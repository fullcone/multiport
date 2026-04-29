# Tailscale Direct Multi-Source UDP - Phase 12 Non-Direct Path Guard

## Scope

Phase 12 closes the peer-relay/Geneve safety edge for Linux source selection.

The implementation already routes source selection through `epAddr.isDirect()` before forced or automatic auxiliary data source selection. This phase adds a dedicated regression test proving that the guard holds for both IPv4 and IPv6 non-direct endpoints when:

- `TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux` would otherwise force auxiliary sends.
- `TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true` has retained auxiliary probe samples.
- The endpoint carries a VNI, which represents a peer relay/Geneve path rather than a direct UDP path.

No production code behavior is changed in this phase.

## PR Review Gate

Before starting this phase, PR #1 had no current unresolved blocking Codex review thread.

The previous Phase 11 runtime-disable cleanup review request:

- Comment: `https://github.com/fullcone/multiport/pull/1#issuecomment-4343308015`
- Codex response: `https://github.com/fullcone/multiport/pull/1#issuecomment-4343329715`
- Result: no major issues.

All known inline review threads were resolved at the time this phase started. Older review threads that remain in the timeline are either resolved or outdated against the current head.

## Phase 12 Review Tracking

Implementation commit:

- `3f4a04935` - `magicsock: guard srcsel from nondirect paths`

Codex review request:

- Comment: `https://github.com/fullcone/multiport/pull/1#issuecomment-4343400529`

First 60-second poll:

- Timestamp: `2026-04-29T19:51:59+08:00`
- Review threads: no new current unresolved blocking thread.
- Conversation: no Codex response to the Phase 12 review request yet.
- Action: continue to the next implementation item because there is no blocking review feedback.

## Implementation

Added `TestSourcePathDataSendSourceNonDirectGuardDualStack` in `wgengine/magicsock/sourcepath_linux_test.go`.

The test constructs:

- A Linux `Conn` with current IPv4 and IPv6 auxiliary source sockets.
- A direct IPv4 endpoint and a direct IPv6 endpoint.
- Matching non-direct IPv4 and IPv6 endpoints with a `packet.VirtualNetworkID` set.

The forced-source section proves:

- Direct IPv4 selects the IPv4 auxiliary socket.
- Direct IPv6 selects the IPv6 auxiliary socket.
- Non-direct IPv4 returns the primary source.
- Non-direct IPv6 returns the primary source.

The automatic-source section then disables forced mode, seeds retained auxiliary probe samples, and proves:

- Direct IPv4 selects the IPv4 auxiliary candidate.
- Direct IPv6 selects the IPv6 auxiliary candidate.
- Non-direct IPv4 still returns the primary source.
- Non-direct IPv6 still returns the primary source.
- The non-direct guard does not mutate pending probe or retained sample state.

## Risk Closed

This records the intended first-version boundary:

- Direct UDP paths may use auxiliary source selection.
- Peer relay/Geneve paths must stay on the existing primary send path.
- Forced auxiliary mode is still constrained by `epAddr.isDirect()`.
- Automatic auxiliary mode cannot use stale or retained VNI samples to steer non-direct data.

This keeps relay/Geneve handling out of the Linux source-selection feature until a separate design explicitly supports it.

## Validation

Run on Linux through WSL:

```text
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/zerotier-client/multiport -- bash -lc 'go test ./wgengine/magicsock -run "TestSourcePathDataSendSourceNonDirectGuardDualStack|TestSourcePathDataSendSourceForcedAuxDualStack|TestSourcePathDataSendSourceAutomaticCandidateDualStack" -count=1 -v'
```

Expected signal:

```text
--- PASS: TestSourcePathDataSendSourceNonDirectGuardDualStack
--- PASS: TestSourcePathDataSendSourceForcedAuxDualStack
--- PASS: TestSourcePathDataSendSourceAutomaticCandidateDualStack
PASS
ok      tailscale.com/wgengine/magicsock
```

Also run:

```text
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/zerotier-client/multiport -- bash -lc 'go test ./wgengine/magicsock -run TestSourcePath -count=1'
git diff --check
```
