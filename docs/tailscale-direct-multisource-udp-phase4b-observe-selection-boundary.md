# Tailscale Direct Multisource UDP Phase 4B Observe Selection Boundary

Date: 2026-04-29

This document records the Phase 4B implementation against
`tailscale_direct_multisource_udp_final_implementation_v02.md`.

## Scope

Phase 4B adds a `Conn`-level observe-only boundary around the Phase 4A
source-path scorer. It still does not enable automatic data-path source
selection.

Implemented for both IP families:

- IPv4 candidate observation uses current IPv4 auxiliary probe sources.
- IPv6 candidate observation uses current IPv6 auxiliary probe sources.
- Candidate observation requires a direct UDP endpoint.
- Candidate observation requires current auxiliary probe source metadata.

Out of scope for this phase:

- changing `endpoint.send` source selection policy
- changing forced auxiliary data-send semantics
- changing primary fallback behavior
- promoting auxiliary probe endpoints into endpoint maps
- changing primary socket rebind behavior

## Behavior

The new boundary is:

```text
Conn.sourcePathBestCandidate(dst)
```

It snapshots current source-path probe sources for the destination IP family,
then scores already-recorded probe samples under the existing `Conn` mutex.

The method returns no candidate when:

- the destination is not direct
- no current auxiliary probe socket exists for the destination IP family
- current auxiliary source metadata does not match recorded samples
- only primary source metadata is available

The method is read-only. It does not mutate pending probes, sample history,
data-send source selection, endpoint state, or primary rebind state.

## Code Changes

`wgengine/magicsock/sourcepath.go`

- Adds `Conn.sourcePathBestCandidate(dst)`.
- Keeps the scoring implementation in `sourcePathProbeManager.bestCandidateLocked`.
- Avoids nested `sourcePath.mu` and `Conn.mu` locking by snapshotting sources
  before acquiring `Conn.mu`.
- Changes `sourcePathSocket.id` to an atomic field and routes rebind/setup
  writes through `sourcePathSocket.setID`, so receive hot-path metadata reads
  cannot race with auxiliary socket rebinds.

`wgengine/magicsock/sourcepath_test.go`

- Adds a race-oriented common test that concurrently updates
  `sourcePathSocket` IDs while reading `rxMeta`.
- Adds a common test proving the `Conn` boundary returns no candidate when
  there are probe samples but no current auxiliary probe source.
- Proves the `Conn` boundary does not mutate pending probes or sample history.

`wgengine/magicsock/sourcepath_linux_test.go`

- Updates the Linux dual-stack observe-only test to call
  `Conn.sourcePathBestCandidate`.
- Proves IPv4 and IPv6 candidates can be observed through the `Conn` boundary
  while forced auxiliary data sends are disabled.
- Proves unforced data sends still select the primary source for IPv4 and IPv6.
- Proves observe-only candidate observation does not touch `lastErrRebind`.

## Safety Properties

Normal data behavior is unchanged. Phase 4B adds no production call from
`endpoint.send` to the observed candidate.

The only current data-send override remains the explicit debug knob:

```text
TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE
```

When that knob is unset, `sourcePathDataSendSource` continues to return the
primary source for both IPv4 and IPv6 direct UDP destinations.

Phase 5 later consumes this boundary only behind the separate explicit
`TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE` gate. The Phase 4B invariant remains
important: scorer observation by itself does not imply data-path selection.

## Validation

Completed locally on 2026-04-29:

```powershell
gofmt -w wgengine\magicsock\sourcepath.go wgengine\magicsock\sourcepath_test.go wgengine\magicsock\sourcepath_linux_test.go
go test ./wgengine/magicsock -run "TestSourcePath" -count=1
go test ./wgengine/magicsock ./envknob -count=1
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'go test ./wgengine/magicsock ./envknob -count=1'
git diff --check
```

Results:

- Windows `go test ./wgengine/magicsock -run "TestSourcePath" -count=1`: passed.
- Windows `go test ./wgengine/magicsock ./envknob -count=1`: passed.
- WSL Ubuntu-24.04 `go test ./wgengine/magicsock ./envknob -count=1`: passed.
- `git diff --check`: passed with only CRLF worktree warnings.

Follow-up validation for the Codex P2 socket ID synchronization fix:

```powershell
go test -race ./wgengine/magicsock -run TestSourcePathSocketRxMetaConcurrentIDUpdate -count=1
$env:CGO_ENABLED='1'; go test -race ./wgengine/magicsock -run TestSourcePathSocketRxMetaConcurrentIDUpdate -count=1
go test ./wgengine/magicsock -run "TestSourcePath" -count=1
go test ./wgengine/magicsock ./envknob -count=1
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'go test -race ./wgengine/magicsock -run TestSourcePathSocketRxMetaConcurrentIDUpdate -count=1'
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'go test ./wgengine/magicsock ./envknob -count=1'
git diff --check
```

Results:

- Windows `go test -race ...`: not runnable with default `CGO_ENABLED=0`.
- Windows `CGO_ENABLED=1 go test -race ...`: not runnable because no `gcc`
  exists in `%PATH%`.
- Windows `go test ./wgengine/magicsock -run "TestSourcePath" -count=1`: passed.
- Windows `go test ./wgengine/magicsock ./envknob -count=1`: passed.
- WSL Ubuntu-24.04 `go test -race ./wgengine/magicsock -run TestSourcePathSocketRxMetaConcurrentIDUpdate -count=1`: passed.
- WSL Ubuntu-24.04 `go test ./wgengine/magicsock ./envknob -count=1`: passed.
- `git diff --check`: passed with only CRLF worktree warnings.

## PR Review Record

Implementation commit:

```text
312a9e12260b0a0a4494a399cbadec5e5d062aa0
```

Review request:

- PR comment: https://github.com/fullcone/multiport/pull/1#issuecomment-4340841513
- Scope requested: Phase 4B observe-only `Conn.sourcePathBestCandidate`
  boundary, IPv4/IPv6 candidate observation, unchanged data-send behavior,
  unchanged primary fallback, unchanged endpoint promotion, unchanged primary
  rebind logic.

Polling status recorded on 2026-04-29:

- First 60-second poll: no Codex response after the Phase 4B review request;
  no new unresolved review thread.
- Second 60-second poll: no Codex response after the Phase 4B review request;
  no new unresolved review thread.
- Third 60-second poll: no Codex response after the Phase 4B review request;
  no new unresolved review thread.

Current audit state:

- All earlier Codex review threads on PR #1 are resolved.
- A later Codex response found one Phase 4B-specific P2 at
  https://github.com/fullcone/multiport/pull/1#discussion_r3158651554:
  `sourcePathSocket.rxMeta` could read `id` concurrently with rebind writes.
- The P2 is valid and is fixed by making `sourcePathSocket.id` atomic and
  adding `TestSourcePathSocketRxMetaConcurrentIDUpdate`, verified under WSL
  Linux `go test -race`.
- After the atomic ID fix, PR thread `PRRT_kwDOSPBZuM5-U8eD` became outdated
  and no new non-outdated Codex review issue was present during the follow-up
  poll.

Post-fix runtime validation on 2026-04-29:

- Revalidated forced auxiliary data send on commit
  `5c6738d84cca8f09d896e82c375a181c97158b8a`.
- WSL Ubuntu-24.04 command:
  `go test ./wgengine/magicsock -run TestSourcePathForcedAuxDualNodeRuntime -count=1 -v`.
- IPv4 proof line:
  `forced aux runtime path: aux=127.0.0.1:34452 primary=127.0.0.1:46258 peer=127.0.0.1:37725`.
  The test then injected an auxiliary `EPERM` and logged primary fallback:
  `srcsel: data send from source 1 to 127.0.0.1:37725 failed, retrying primary: write: operation not permitted`.
- IPv6 proof line:
  `forced aux runtime path: aux=[::1]:45730 primary=[::1]:39643 peer=[::1]:60969`.
  The test then injected an auxiliary `EPERM` and logged primary fallback:
  `srcsel: data send from source 2 to [::1]:60969 failed, retrying primary: write: operation not permitted`.
- WSL Ubuntu-24.04 package regression:
  `go test ./wgengine/magicsock ./envknob -count=1`: passed.
