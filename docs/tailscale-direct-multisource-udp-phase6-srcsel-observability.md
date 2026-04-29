# Tailscale Direct Multisource UDP Phase 6 Source Selection Observability

Date: 2026-04-29

This document records the Phase 6 implementation against
`tailscale_direct_multisource_udp_final_implementation_v02.md`.

## Scope

Phase 6 adds audit counters to the direct UDP source-path data-send path.

Implemented for both IP families:

- IPv4 forced auxiliary data sends.
- IPv6 forced auxiliary data sends.
- IPv4 automatic auxiliary data sends.
- IPv6 automatic auxiliary data sends.
- Auxiliary send fallback to the primary socket after an auxiliary send error.

Out of scope for this phase:

- enabling source-path data send by default
- changing forced source selection semantics
- changing automatic candidate scoring
- changing primary socket rebind behavior
- changing endpoint selection or endpoint maps

## Metrics

`magicsock_srcsel_data_send_aux_selected`

- Increments once per direct UDP data-send batch when source selection chooses
  a non-primary auxiliary source.
- Counts both forced and automatic auxiliary source selection.

`magicsock_srcsel_data_send_aux_succeeded`

- Increments once per direct UDP data-send batch when an auxiliary send is
  selected and the auxiliary send returns nil.

`magicsock_srcsel_data_send_aux_fallback`

- Increments once per direct UDP data-send batch when an auxiliary send is
  selected, the auxiliary send returns an error, and the code attempts the
  existing primary retry path.
- The counter records that fallback was attempted. The primary retry can still
  return its own error afterward.

These counters are batch-level audit counters, not packet counters.

## Code Changes

`wgengine/magicsock/magicsock.go`

- Adds the three clientmetric counters listed above.

`wgengine/magicsock/endpoint.go`

- Records `aux_selected` immediately after direct UDP data-source selection
  chooses a non-primary source.
- Records `aux_succeeded` when the auxiliary batch send succeeds.
- Records `aux_fallback` before retrying the same batch through the primary
  send path after an auxiliary send error.

`wgengine/magicsock/sourcepath_linux_test.go`

- Extends the forced auxiliary dual-node runtime test to assert selected,
  succeeded, and fallback metric deltas for IPv4 and IPv6.
- Extends the automatic auxiliary dual-node runtime test to assert selected,
  succeeded, and fallback metric deltas for IPv4 and IPv6.

## Safety Properties

The Phase 6 counters do not change source selection, endpoint selection, socket
selection, or fallback behavior.

The primary rebind boundary is unchanged:

- Auxiliary send failure still retries through the primary send path.
- The auxiliary fallback path does not call `maybeRebindOnError`.
- The runtime tests continue to assert that `lastErrRebind` is unchanged after
  successful auxiliary sends and after auxiliary fallback.

The primary endpoint health boundary is unchanged:

- `noteBadEndpoint` remains gated on `usedPrimarySend`.
- Auxiliary-only errors are not treated as primary endpoint failures unless the
  send path actually falls back to primary and the primary send returns a bad
  endpoint error.

The direct-UDP boundary is unchanged:

- Metrics are emitted only inside the existing direct UDP send branch.
- DERP and non-direct endpoint paths are not instrumented by these counters.

## Validation

Completed locally on 2026-04-29:

```powershell
gofmt -w wgengine\magicsock\endpoint.go wgengine\magicsock\magicsock.go wgengine\magicsock\sourcepath_linux_test.go
go test ./wgengine/magicsock -run "TestSourcePath" -count=1
go test ./wgengine/magicsock ./envknob -count=1
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/zerotier-client/multiport -- bash -lc 'go test ./wgengine/magicsock -run "TestSourcePath(ForcedAuxDualNodeRuntime|AutomaticAuxDualNodeRuntime)" -count=1 -v'
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/zerotier-client/multiport -- bash -lc 'go test ./wgengine/magicsock ./envknob -count=1'
git diff --check
```

Results:

- Windows `go test ./wgengine/magicsock -run "TestSourcePath" -count=1`:
  passed.
- Windows `go test ./wgengine/magicsock ./envknob -count=1`: passed.
- WSL Ubuntu-24.04
  `go test ./wgengine/magicsock -run "TestSourcePath(ForcedAuxDualNodeRuntime|AutomaticAuxDualNodeRuntime)" -count=1 -v`:
  passed.
- WSL Ubuntu-24.04 `go test ./wgengine/magicsock ./envknob -count=1`:
  passed.
- `git diff --check`: passed. Git reported expected CRLF working-copy
  warnings on touched Go files.

Runtime proof lines:

- IPv4 forced selection:
  `forced aux runtime path: aux=127.0.0.1:45424 primary=127.0.0.1:50110 peer=127.0.0.1:46804`.
- IPv4 forced fallback after injected auxiliary `EPERM`:
  `srcsel: data send from source 1 to 127.0.0.1:46804 failed, retrying primary: write: operation not permitted`.
- IPv6 forced selection:
  `forced aux runtime path: aux=[::1]:41961 primary=[::1]:54574 peer=[::1]:44647`.
- IPv6 forced fallback after injected auxiliary `EPERM`:
  `srcsel: data send from source 2 to [::1]:44647 failed, retrying primary: write: operation not permitted`.
- IPv4 automatic selection:
  `automatic aux runtime path: aux=127.0.0.1:35635 primary=127.0.0.1:43159 peer=127.0.0.1:49424 source={socketID:1 generation:1}`.
- IPv4 automatic fallback after injected auxiliary `EPERM`:
  `srcsel: data send from source 1 to 127.0.0.1:49424 failed, retrying primary: write: operation not permitted`.
- IPv6 automatic selection:
  `automatic aux runtime path: aux=[::1]:39316 primary=[::1]:37121 peer=[::1]:39772 source={socketID:2 generation:1}`.
- IPv6 automatic fallback after injected auxiliary `EPERM`:
  `srcsel: data send from source 2 to [::1]:39772 failed, retrying primary: write: operation not permitted`.

## PR Review Record

Implementation commit:

- `83ed931cb87553b06ed6fc31918f10f50075069a`
  (`magicsock: add source path data send metrics`)

Review request:

- PR #1 comment:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4342447412`

Review result:

- Codex no-major-issues response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4342469162`.

Polling record on 2026-04-29:

- First 60-second poll after the Phase 6 review request: no Codex response for
  commit `83ed931cb87553b06ed6fc31918f10f50075069a`; no new actionable review
  thread appeared.
- Later poll found the Codex no-major-issues response for the Phase 6
  implementation review request.
- Doc-only polling-record commit:
  `9f037901dee21e9e18e4440e54f1765919b1231b`
  (`docs: record phase6 review polling`).
- Doc-only polling-record review request:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4342486594`.
- Doc-only polling-record review result:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4342497823`.

Current PR feedback state after the Phase 6 review responses:

- Earlier Phase 2 review threads are resolved.
- Earlier Phase 3 test-fixture review threads are resolved and outdated.
- The only unresolved thread is the outdated Phase 4B P2
  `sourcePathSocket.rxMeta` synchronization finding
  (`PRRT_kwDOSPBZuM5-U8eD`), already fixed by commit
  `5c6738d84cca8f09d896e82c375a181c97158b8a` and followed by a no-major-issues
  Codex review.
- No Phase 6-specific actionable feedback has appeared.
