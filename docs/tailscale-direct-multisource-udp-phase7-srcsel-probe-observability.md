# Tailscale Direct Multisource UDP Phase 7 Source Probe Observability

Date: 2026-04-29

This document records the Phase 7 implementation against
`tailscale_direct_multisource_udp_final_implementation_v02.md`.

## Scope

Phase 7 adds clientmetric counters for the source-path probe lifecycle. The
goal is to make probe health visible during review and runtime validation
without changing source selection behavior.

Implemented for both IP families:

- accepted auxiliary source-path Pong replies
- timeout pruning of unsatisfied auxiliary probe TxIDs
- late auxiliary Pong replies that match an expired pending probe

Out of scope for this phase:

- changing probe scheduling
- changing probe packet format
- changing candidate scoring
- changing forced or automatic data-source selection
- changing primary socket rebind behavior

## Metrics

`magicsock_srcsel_probe_pong_accepted`

- Increments once when an auxiliary source-path Pong matches a pending probe
  by TxID, source metadata, and destination disco key.
- Counts only accepted samples that are recorded for candidate scoring.

`magicsock_srcsel_probe_pending_expired`

- Increments by the number of pending auxiliary probe TxIDs pruned by timeout
  during source-path probe insertion.
- Uses the existing `pingTimeoutDuration` boundary.

`magicsock_srcsel_probe_pong_expired`

- Increments once when a Pong arrives for a pending auxiliary probe whose TxID
  is known but whose send time is already past `pingTimeoutDuration`.
- The expired Pong is consumed so it cannot fall through into primary Pong
  handling, but no candidate sample is recorded.

These counters are source-path probe lifecycle counters, not packet counters.

## Code Changes

`wgengine/magicsock/magicsock.go`

- Adds the three clientmetric counters listed above.

`wgengine/magicsock/sourcepath.go`

- Records timeout-pruned pending probe TxIDs in
  `sourcePathProbeManager.pruneExpiredLocked`.
- Records expired auxiliary Pong replies in
  `sourcePathProbeManager.handlePongLocked`.
- Records accepted auxiliary Pong replies after the sample is appended and the
  history cap is applied.

`wgengine/magicsock/sourcepath_test.go`

- Covers accepted Pong metric deltas.
- Covers timeout-pruned pending probe metric deltas.
- Covers expired Pong metric deltas and asserts expired Pongs do not increment
  the accepted-Pong counter.

## Safety Properties

The Phase 7 counters do not change source selection, endpoint selection,
socket selection, or fallback behavior.

The source-path probe state boundary is unchanged:

- Primary socket Pongs still do not consume auxiliary pending probes.
- Mismatched auxiliary source metadata still does not consume pending probes.
- Mismatched destination disco keys still do not consume pending probes.
- Accepted auxiliary Pongs still record the same sample data as before.
- Expired auxiliary Pongs still remove the pending TxID without recording a
  sample.

The timeout boundary is unchanged:

- Pending source-path probe TxIDs use the existing `pingTimeoutDuration`.
- The new pending-expired metric is emitted only when existing timeout pruning
  deletes entries.

## Validation

Completed local validation on 2026-04-29:

```powershell
gofmt -w wgengine\magicsock\magicsock.go wgengine\magicsock\sourcepath.go wgengine\magicsock\sourcepath_test.go
go test ./wgengine/magicsock -run "TestSourcePathProbe" -count=1
go test ./wgengine/magicsock ./envknob -count=1
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'go test ./wgengine/magicsock -run "TestSourcePathProbe" -count=1'
git diff --check
```

Results:

- Windows focused source-path probe tests passed.
- Windows package validation for `./wgengine/magicsock` and `./envknob`
  passed.
- WSL Ubuntu-24.04 focused source-path probe tests passed. WSL printed a
  localhost/NAT warning before the test run, but the Go test process exited
  successfully.
- `git diff --check` passed with only CRLF worktree conversion warnings on
  touched Go files.

## PR Review Record

Implementation commit:

- `7d11322ad68d263e22def0cd42bb3738a4b9f93a`

Review request:

- `https://github.com/fullcone/multiport/pull/1#issuecomment-4342686734`

First 60-second poll after review request:

- No Codex response had arrived yet for the Phase 7 implementation review
  request.
- No new actionable review thread was present.
- The only unresolved thread remained the outdated Phase 4B P2
  `sourcePathSocket.rxMeta` synchronization finding
  (`PRRT_kwDOSPBZuM5-U8eD`), already fixed by commit
  `5c6738d84cca8f09d896e82c375a181c97158b8a`.

Doc-only polling record:

- Commit `e48c29d7238b95b53a3172576f709f7fe8603e31` recorded the Phase 7
  implementation commit, review request, and first poll result.
- Review request:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4342729421`.
- The next 60-second poll found a Codex no-major-issues response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4342739008`.
- That poll still showed no new actionable review thread; the only unresolved
  thread remained the outdated Phase 4B P2 `sourcePathSocket.rxMeta`
  synchronization finding already fixed by
  `5c6738d84cca8f09d896e82c375a181c97158b8a`.

Current PR feedback state before this implementation:

- The latest Phase 6 doc-only review-result commit
  `a6f717ba188b67dc8bfe3fb04b38bd2d9151d5fd` received a Codex no-major-issues
  response at
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4342569210`.
- Earlier Phase 2 review threads are resolved.
- Earlier Phase 3 test-fixture review threads are resolved and outdated.
- The only unresolved thread is the outdated Phase 4B P2
  `sourcePathSocket.rxMeta` synchronization finding
  (`PRRT_kwDOSPBZuM5-U8eD`), already fixed by commit
  `5c6738d84cca8f09d896e82c375a181c97158b8a` and followed by a no-major-issues
  Codex review.
- No new actionable review thread blocked Phase 7.
