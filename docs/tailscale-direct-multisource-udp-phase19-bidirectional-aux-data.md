# Tailscale Direct Multisource UDP Phase 19 Bidirectional Auxiliary Data

Date: 2026-04-30

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\zerotier-client\multiport`

WSL checkout: `/mnt/c/other_project/zerotier-client/multiport`

Branch: `phase1-srcsel-source-metadata`

Pull request: `https://github.com/fullcone/multiport/pull/1`

Phase 19 commits (oldest to newest, on top of Phase 18 head `982a583b6`):

- `2bb1d6779` magicsock: deliver auxiliary WireGuard receive instead of dropping
- `0980acb59` magicsock: harden srcsel scorer with TTL, min samples, mean latency
- `94c709570` magicsock: lift srcsel policy caps, add memory-safety hard caps
- `86a52a532` magicsock: separate srcsel padded probe metrics from peer MTU probes

## Why Phase 19

A re-review after Phase 18 closeout flagged an architectural inconsistency
between the auxiliary send and receive paths and a number of supporting
issues. The user requested all of them be fixed in continued PR #1 commits
rather than a new PR. This document records the changes in one place so the
Phase 16 closeout language can be read with the Phase 19 corrections in
mind.

## Architectural Issue Phase 19 Resolves

Before Phase 19, `Conn.endpoint.send` would call `sendUDPBatchFromSource`
with an auxiliary source socket whenever forced or automatic source
selection picked it, but `Conn.receiveIPWithSource` unconditionally
discarded any WireGuard-shaped packet that arrived on an auxiliary socket:

```
if !rxMeta.isPrimary() && pt == packetLooksLikeWireGuard {
    return nil, 0, false, false
}
```

This meant the auxiliary path was outbound-only at the data plane. If a
remote peer's WireGuard implementation roamed its endpoint to the local
auxiliary `<ip>:<port>` after receiving an authenticated packet from there,
its replies would be silently dropped on receive while local metrics
continued to report the outbound write as successful. The result was a
single-direction "looks healthy locally, blackholed on return" failure
mode that no existing test caught because `transitOnePing` only verified
arrival at the destination TUN and did not exercise the reverse path.

Phase 19 makes auxiliary data flow symmetrical:

- `receiveIPWithSource` no longer drops WireGuard-shaped packets that
  arrive on an auxiliary socket. They take the same `lazyEndpoint` /
  `peerMap` path as primary receive. WireGuard endpoint roaming via
  `lazyEndpoint.FromPeer` continues to use the remote address as the
  peerMap key, which is independent of the local source socket choice, so
  no new endpoint pollution is introduced.
- A `magicsock_srcsel_aux_wireguard_rx` counter tracks how often
  auxiliary receive saw a WireGuard-shaped frame (previously the count
  of dropped frames; now the count of admitted frames).

## Scorer Hardening

The previous scorer picked the source with the lowest historical latency
over a 32-sample global FIFO buffer, with no TTL and no minimum sample
count. A single lucky probe could pin auxiliary selection for an
arbitrarily long time, and at scale the FIFO ring guaranteed only
`limit / N` samples per (dst, source) pair so most peers never reached
any usable scoring threshold.

Phase 19 changes:

- `sourcePathSampleTTL = 60 * time.Second`. The scorer skips samples
  older than the TTL.
- `sourcePathMinSamplesForUse = 3`. Automatic selection refuses to use a
  (dst, source) pair until it has accumulated at least three TTL-fresh
  samples.
- The scorer averages latency across TTL-fresh samples instead of using
  the historical minimum, so a representative figure drives the
  selection.
- New `Conn.noteSourcePathSendFailure` invalidates a (dst, source)
  pair's samples whenever a real-data send through that source fails and
  the caller falls back to primary. The scorer is then forced to wait
  for fresh probe evidence before steering data back through the failed
  pair. Wired in `endpoint.send` between the auxiliary attempt and the
  primary fallback.

## Limit Relaxation And Memory Safety

The Phase 8 safety budget (32 peers, burst 1) was tuned for client-side
NAT scenarios where conntrack pressure and IDS heuristics matter. For
the server deployments PR #1 targets, those caps prevented IPv4 and
IPv6 samples from being collected concurrently for the same peer and
cut most peers off from auxiliary selection entirely.

Phase 19 replaces the policy caps with permissive defaults gated only
on memory:

- `sourcePathProbeMaxPeers = 0` — no policy cap on distinct peers by
  default. `TS_EXPERIMENTAL_SRCSEL_MAX_PEERS` can still set an explicit
  positive cap if needed.
- `sourcePathProbeMaxBurst = 8` — allows IPv4 + IPv6 plus a few
  concurrent probes per peer. Tighter operational caps may override via
  `TS_EXPERIMENTAL_SRCSEL_MAX_PROBE_BURST`.
- `sourcePathProbeHardPendingCap = 100000` — memory-safety hard cap on
  the total pending probe map. Disco peers that never recognize
  `SourcePathProbe` cause pending probes to expire normally; this cap
  exists only as a backstop. Override via
  `TS_EXPERIMENTAL_SRCSEL_MAX_PENDING`.
- `sourcePathProbeHistoryLimit` raised from 32 to 100000 and
  reinterpreted as a hard memory bound. Freshness is now enforced by
  `pruneExpiredSamplesLocked` on every accepted Pong, not by a global
  FIFO ring. Override via `TS_EXPERIMENTAL_SRCSEL_MAX_SAMPLES`.

`addWithBudgetLocked` grows a `hardCap` argument; existing callers in
the test suite pass `0` to disable it when exercising policy budgets.

## Metric Attribution

`sendSourcePathDiscoPing` previously incremented
`magicsock_disco_sent_peer_mtu_probes` and `..._peer_mtu_probe_bytes`
when the probe carried padding. SourcePathProbe shares the padding
mechanism with disco peer-MTU probes but is not the same thing, and
folding them into the peer-MTU counters poisoned that dashboard once
srcsel was enabled.

Phase 19 adds dedicated counters and stops touching the peer-MTU
counters from `sendSourcePathDiscoPing`:

- `magicsock_disco_sent_source_path_probe_padded`
- `magicsock_disco_sent_source_path_probe_bytes`

## Status Of Phase 16 "Behavior Now Guaranteed In Scope"

Phase 16's "Behavior Now Guaranteed In Scope" section was written
before Phase 19 confirmed bidirectional auxiliary data via removal of
the receive drop. Read those guarantees together with this Phase 19
record:

- "Direct peer data traffic may use a selected auxiliary source socket"
  is now true for both directions of the data plane, not just the
  outbound write.
- "Forced auxiliary data sends" and "Automatic auxiliary data
  selection" are still in scope, but automatic selection now requires
  three TTL-fresh probe samples per (dst, source) before it will steer
  real data, and the manager invalidates samples on send failure.
- "Auxiliary disco probes do not advertise auxiliary endpoints as
  normal peer candidates" remains true. WireGuard's own endpoint
  roaming may still associate the remote with our auxiliary address in
  `peerMap`; this is the standard roaming behavior and is now safe
  because auxiliary receive no longer drops WireGuard frames.

## Tests Added

- `TestReceiveIPAuxiliaryAcceptsWireGuard` — directly drives a
  WireGuard-shaped packet through `receiveIPWithSource` with auxiliary
  metadata and asserts the function returns a `lazyEndpoint` with
  `ok=true` and increments
  `magicsock_srcsel_aux_wireguard_rx`.
- `TestSourcePathProbeManagerSkipsExpiredSamples` — asserts the
  scorer skips samples older than `sourcePathSampleTTL`.
- `TestSourcePathProbeManagerRequiresMinSamplesForUse` — asserts the
  N=3 gate.
- `TestSourcePathProbeManagerInvalidateDropsMatching` — asserts
  invalidation drops matching samples and only matching samples.
- `TestConnNoteSourcePathSendFailureClearsSamples` — asserts the
  `Conn`-level send-failure path invalidates and meters correctly,
  with the primary source as a no-op.
- `TestSourcePathProbeManagerUnlimitedPeersByDefault` — asserts that
  passing `maxPeers = 0` accepts arbitrary peer counts.
- `TestSourcePathProbeManagerEnforcesHardPendingCap` — asserts the
  memory-safety hard cap rejects further pending probes once full.
- `TestSourcePathProbeManagerSamplePruneOnPongAndCap` — asserts
  TTL-stale samples are dropped by `pruneExpiredSamplesLocked`.

Existing scorer tests were updated to seed at least
`sourcePathMinSamplesForUse` per (dst, source) pair and to assert mean
latency rather than minimum.

## Validation

`go test ./wgengine/magicsock -count=1 -timeout 300s` passes in the WSL
Ubuntu-24.04 environment with Go 1.26.2 (suite runs in ~10.5s).

## Out Of Scope For Phase 19

- Real remote-host bidirectional aux WireGuard validation. The new
  `magicsock_srcsel_aux_wireguard_rx` counter is the basis for
  observation in real deployments; a deliberate end-to-end test
  requires roaming behavior beyond the magicsock harness.
- Primary-baseline RTT comparison in the scorer. The current scorer is
  absolute (it does not compare auxiliary mean latency to primary). A
  later phase may add a primary-baseline gate if real-network data
  shows automatic selection picking auxiliary when primary is in fact
  better.
- Additional Windows runtime evidence beyond what Phases W2 and W6
  recorded.
