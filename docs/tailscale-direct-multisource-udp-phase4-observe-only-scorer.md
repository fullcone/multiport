# Tailscale Direct Multisource UDP Phase 4A Observe-Only Scorer

Date: 2026-04-29

This document records the Phase 4A implementation against
`tailscale_direct_multisource_udp_final_implementation_v02.md`.

## Scope

Phase 4A adds an observe-only scorer for source-path probe samples. It does
not enable automatic data-path source selection.

Implemented for both IP families:

- IPv4 candidates are scored from IPv4 auxiliary probe samples.
- IPv6 candidates are scored from IPv6 auxiliary probe samples.
- Candidates are considered only when the destination is a direct UDP endpoint.
- Candidate samples must match the exact destination and exact `sourceRxMeta`.

Out of scope for this phase:

- changing `endpoint.send` source selection policy
- changing `sourcePathDataSendSource`
- changing `bestAddr`, peer endpoint maps, or disco endpoint promotion
- routing normal data sends through the scorer
- changing primary socket rebind behavior

## Behavior

The scorer is implemented as
`sourcePathProbeManager.bestCandidateLocked(dst, sources)`.

It returns a `sourcePathCandidateScore` containing:

- selected auxiliary source metadata
- best observed latency for that source and destination
- number of matching samples for that source and destination
- most recent sample time for that source and destination

Selection rules:

- Primary source metadata is ignored.
- Non-direct destinations are ignored.
- Samples for other destinations are ignored.
- Samples for other source metadata are ignored.
- Stale-generation samples are ignored when the caller passes only current
  source metadata.
- The lowest latency candidate wins.
- If latency ties, the candidate with the most recent matching sample wins.

The scorer is read-only. It does not mutate pending probes, sample history,
endpoint state, data-send source selection, or primary rebind state.

## Code Changes

`wgengine/magicsock/sourcepath.go`

- Adds `sourcePathCandidateScore`.
- Adds `sourcePathProbeManager.bestCandidateLocked`.
- Keeps scoring local to already-recorded source-path probe samples.

`wgengine/magicsock/sourcepath_test.go`

- Adds a dual-stack observe-only scorer unit test.
- Proves IPv4 and IPv6 candidates are selected from exact endpoint plus exact
  source metadata matches.
- Proves primary samples, wrong-destination samples, and nonmatching-generation
  samples do not become candidates.
- Proves scoring does not mutate pending probes or sample history.

`wgengine/magicsock/sourcepath_linux_test.go`

- Adds a Linux-only dual-stack integration-level scorer test over current
  auxiliary source metadata returned by `sourcePathProbeSources`.
- Proves IPv4 and IPv6 scoring works while `TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE`
  is unset.
- Proves unforced data sends still select the primary source for IPv4 and IPv6.
- Proves observe-only scoring does not touch `lastErrRebind`.

## Safety Properties

Normal behavior is unchanged. Phase 4A adds no production call from the direct
WireGuard send path to the scorer.

The only current data-send override remains the explicit Phase 3 debug knob:

```text
TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE
```

When that knob is unset, `sourcePathDataSendSource` continues to return the
primary source for both IPv4 and IPv6 direct UDP destinations.

The scorer also does not promote auxiliary probe endpoints into peer endpoint
maps. Phase 2 source-path probe isolation and Phase 3 primary fallback behavior
remain unchanged.

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
