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

`wgengine/magicsock/sourcepath_test.go`

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
