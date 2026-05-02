# Tailscale srcsel ROADMAP

Forward-looking work items not yet scheduled into a phase. Items here
are *candidate* designs, not commitments. They become real PRs only
when picked up by a phase doc.

---

## Phase 21 (Implemented in PR #17, #18) — Dynamic multi-endpoint advertise

**Problem.** A `tailscaled` server today advertises a set of endpoints
that magicsock's `determineEndpoints` (`wgengine/magicsock/magicsock.go`)
gathers from a fixed list of *intrinsic* sources: STUN responses,
NAT-PMP / PCP / UPnP port-mapping, locally-bound interface addresses,
and a small static-config slot. The set is fanned out to peers via the
control client's `cc.UpdateEndpoints` → `MapRequest.Endpoints` /
`MapRequest.EndpointTypes` flow (`control/controlclient/auto.go`),
landing on the control plane as `tailcfg.Node.Endpoints` and
propagating outward as full netmap updates or
`tailcfg.PeersChangedPatch.Endpoints` deltas.

What is **missing** is a way to inject endpoints that live behind a
DNAT layer that is **not** discoverable by any of those intrinsic
mechanisms — e.g. multiple public IP:port front doors managed by a
separate load-balancer / "rotating IPs against censorship" controller
that DNATs `P1:port → S:port`, `P2:port → S:port`, etc. STUN cannot
see those alternate front doors (the server's own STUN exchange goes
out via *one* of them, returning *one* mapped tuple). Port-mapping
protocols target the local NAT, not an upstream LB. Static config is
restart-only. So extra DNAT rules are dead weight even though the
underlying packets, if a client knew to send them, would arrive
correctly at the server's primary socket.

**Why this is *not* covered by Phase 1-20 srcsel.** Source-path
selection (Phases 1-20 + W-series validation) is about the **sender**
choosing which of its multiple local UDP sockets to send from. It
does not change which set of `(IP:port)` *destinations* the receiver
advertises to its peers, nor whether those destinations include
addresses that live on a separate machine.

**User scenario making this concrete.** The operator wants:

- A pool of public IP:port front doors managed externally (DNAT
  table edited live by a separate system, e.g. a load-balancer
  control plane or a "rotating IPs against censorship" controller).
- Whenever the pool changes, **already-connected clients** should
  see the new IP:port set within seconds.
- Newly connecting clients should pick from the latest pool.

That dynamic case is the load-bearing requirement; a static one-shot
`--advertise-endpoints` flag would be insufficient.

### Sketch

1. **Source of truth**: a small JSON or line-oriented file on the
   server, e.g. `/etc/tailscaled/extra-endpoints.json`:
   ```json
   {
     "endpoints": ["P1:41641", "P2:41641", "P3:41641"]
   }
   ```
2. **Watcher inside magicsock's gather step**:
   `Conn.determineEndpoints` already returns a `[]tailcfg.Endpoint`
   built from `tailcfg.EndpointSTUN`, `EndpointPortMapped`,
   `EndpointLocal`, `EndpointExplicitConf`, etc. Add a new
   `EndpointExtraAdvertised` (or extend the `EndpointExplicitConf`
   handling) with values read from this file. A `fsnotify`-based
   watcher (Linux inotify / macOS FSEvents / Windows
   ReadDirectoryChangesW) running in its own goroutine calls
   `Conn.ReSTUN("extra-endpoints-changed")` on file change so the
   normal endpoint-update path picks up the new set without bypassing
   `setEndpoints` deduplication / change detection.
3. **Control plane propagation**: re-uses the *existing* path —
   `setEndpoints` → `epFunc` callback registered by `wgengine`
   (`wgengine/userspace.go`) → `cc.UpdateEndpoints` (in
   `ipnlocal/local.go`) → `controlclient.Auto.UpdateEndpoints` →
   `Direct.sendMapRequest` → over the wire as `MapRequest.Endpoints`
   + `MapRequest.EndpointTypes` (`tailcfg.MapRequest`). The control
   plane stores the set in `tailcfg.Node.Endpoints` and fans it out
   to peers via either a fresh netmap or a
   `tailcfg.PeersChangedPatch.Endpoints` delta. **No new
   control-plane field is required.**
4. **Client-side application**: peers' magicsock receives the netmap
   update at `magicsock.go:4024` and calls
   `ep.setEndpointsLocked(views.SliceOf(m.Endpoints))`, which already
   handles the multi-endpoint set. The next path probe / ping will
   try the new IP:port, no client-side code change needed.
5. **Polling fallback**: for filesystems where fsnotify is unreliable
   (e.g. some network mounts), allow a polling interval via env
   `TS_EXPERIMENTAL_EXTRA_ENDPOINTS_POLL=10s`.
6. **Removal semantics**: when an endpoint disappears from the file,
   `determineEndpoints` returns a smaller set, `setEndpoints` reports
   it changed, and the same flow propagates the removal. Peers
   whose `bestAddr` was the removed endpoint detect failure on next
   send (or via `noteRecvActivity` quiet timeout) and re-probe.
7. **End-to-end timing**: the netmap update reaches peers within ~1 s
   in the streaming long-poll path; the file-change → wire latency is
   bounded by the file watcher (sub-second on inotify, polling
   interval otherwise) plus the next `determineEndpoints` cycle.

### Open questions

- **Authentication for extra endpoints.** WireGuard handshake at the
  destination still authenticates the actual data plane regardless of
  how the client learned the IP:port, so the wire-level threat model
  is unchanged. The *new* surface is local: any process that can write
  the file can publish endpoints under this node's identity. The
  file's filesystem permissions therefore become the trust boundary.
  Plan: chmod 0644 owner=root by default, refuse to read if
  group-writable or world-writable, log loudly on permission widening.
  A future variant could read from a Unix socket controlled by a
  sibling daemon instead of a flat file.
- **Interaction with srcsel auxiliary sockets.** If the server has
  aux sockets enabled, do we advertise extra endpoints for the aux
  socket too? Probably yes — the watcher should publish
  `(extra_ip:port, primary_socket_id)` and
  `(extra_ip:port, aux_socket_id)` if both sockets are bound on
  `0.0.0.0` and the kernel will accept on either.
- **Endpoint cap.** magicsock has soft limits on how many endpoints
  it carries per peer. The file watcher should cap at a sane number
  (suggest 32) and log when exceeded; document the cap in
  `TS_EXPERIMENTAL_EXTRA_ENDPOINTS_MAX`.
- **Probe storm avoidance.** A change of 5 endpoints in the file
  could, on the receive side, kick off 5 source-path probes per
  source socket per peer. Worth a deliberate think before merging:
  borrow the Phase 8 `addWithBudgetLocked` machinery so the first
  change drains within budget and subsequent rapid changes coalesce.

### Out of scope for the candidate

- Multiple distinct WireGuard *node identities* on the same machine
  (already possible by running multiple `tailscaled` processes; not
  what this phase is about).
- Per-client endpoint policy ("client A goes via P1, client B via
  P2"). The file watcher publishes one set; client-side selection
  is unchanged.
- Failover-aware health checking of the extra endpoints. The control
  plane is the source of truth; the operator's external
  load-balancer is responsible for keeping the file in sync with
  reality.

### Estimated effort

- magicsock watcher + endpoint gather hook: ~150 LoC Go.
- env knob plumbing + tests: ~50 LoC Go + ~80 LoC test.
- Phase doc + bilateral validation: similar to W7 / W10 / W11 / W12
  scope (~300 LoC Python + 1 phase doc).
- Total: roughly the size of Phase 8 + W10 combined.

---

## Phase 22 (Implemented in PR #16) — Direct-vs-relay latency-aware switching

**Problem.** Tailscale's current `endpoint.wantUDPRelayPathDiscoveryLocked`
(`wgengine/magicsock/endpoint.go:912-947`) unconditionally prefers a
direct UDP path over any peer-relay path:

```go
if de.bestAddr.isDirect() && now.Before(de.trustBestAddrUntil) {
    return false  // suppress relay path discovery
}
```

When `bestAddr` holds a trusted direct path, relay-path discovery is
skipped entirely — magicsock never even *measures* relay latency for
comparison. This is the right default for the common case (direct
UDP is usually lower-latency and higher-throughput than any relay),
but it leaves a real gap when:

1. The direct UDP path traverses a long internet detour (e.g.
   client and server are on the same continent but the BGP path
   their ISPs choose loops through a different continent — typical
   for some Asia-Pacific peering arrangements).
2. A peer-relay sits closer to both endpoints than their direct
   internet path is to each other. The total `A → relay → B → relay
   → A` latency is lower than `A → B direct` RTT.

In those cases the operator wants Tailscale to recognize that the
relay is actually faster and switch — but the current code path
never gives the relay a chance, because it never probes the relay
once direct is established.

The TODO already in the source (`endpoint.go:933-939`) explicitly
flags this:

> consider applying 'goodEnoughLatency' suppression here, but not
> until we have a strategy for triggering CallMeMaybeVia regularly
> and/or enabling inbound packets to act as a UDP relay path
> discovery trigger ...

Phase 22 is the inverse direction of that TODO: rather than "suppress
relay discovery harder when direct is good enough", it adds
"periodic relay-path comparison even when direct is good enough", so
that magicsock can opt in to actively switching when the relay is
*better*.

**Why this is *not* covered by anything already shipped.**

- `relayManager.handshakeServerEndpoint` *can* measure A→relay→C→relay→A
  end-to-end latency through a relay candidate (`relaymanager.go:947 +
  997-1010`), and `endpoint.udpRelayEndpointReady(addrQuality{...,
  latency, ...})` (`relaymanager.go:739-744`) feeds that into
  `bestAddr` ranking. **But** `bestAddr` ranking only ever
  *receives* relay latencies when relay discovery actually runs — and
  relay discovery is suppressed by the gate above whenever a trusted
  direct path exists.
- `TS_DEBUG_NEVER_DIRECT_UDP` (`debugknobs.go:65-68`) is the
  hard switch: it disables direct UDP entirely. That's "force always
  relay" rather than "compare and choose"; it loses direct's
  bandwidth advantage even when direct *would* be the better choice.
- ZeroTier (`node/Topology.cpp::getUpstreamPeer` +
  `node/Peer.hpp::relayQuality`) ranks upstream-peer relays by
  single-segment `A → relay` RTT only and does not do
  direct-vs-relay comparison either.

**Why this is *not* the same as the (withdrawn) old Phase 22.** A
prior Phase 22 candidate (PR #14, withdrawn 2026-05-01 after Codex
P1) claimed the existing relay scoring is single-segment-only;
that was factually wrong. The actual gap — and the one this
candidate addresses — is that the relay scoring is correct *when it
runs*, but is suppressed by the unconditional direct preference. The
fix is at the scheduling layer (decide when to probe relays), not
the measurement layer.

### Sketch

1. **Lift the unconditional direct suppression** in
   `wantUDPRelayPathDiscoveryLocked`. Keep the existing rate-limit
   (`discoverUDPRelayPathsInterval`) but remove the
   `bestAddr.isDirect() ⇒ return false` short-circuit when the
   experimental knob is on. Replace it with a longer comparison
   interval (suggest 5 min) so direct-on-direct workloads pay only
   minimal overhead.
2. **Lift the unconditional direct preference in `betterAddr`**
   (`endpoint.go:1898-1905`). Today a non-Geneve (direct) path
   beats a Geneve-encapsulated (relay) path *before* the
   points-based latency scoring runs, so even if step (1) gives the
   pool both candidates, the existing `betterAddr` will still pick
   direct unconditionally. Under the new env knob, replace the hard
   `vni.IsSet()` short-circuit with a Phase 20-style 10 % relative
   gate that applies to the cross-category transition:
     - currently direct, relay candidate's mean latency
       < `direct.latency × (1 - 10 %)` ⇒ relay wins.
     - currently relay, direct candidate's mean latency
       < `relay.latency × (1 - 10 %)` ⇒ direct wins.
     - otherwise category preference (direct first) is preserved.
   With the experimental knob *off*, today's behaviour is unchanged
   bit-for-bit.
   Without this step, `bestAddr` ranking still picks direct even if
   the relay's measured latency is lower — both step (1) and step
   (2) are needed to actually achieve the proposed switching
   behaviour. The 60-s sample TTL (`sourcePathSampleTTL`) is the
   natural smoothing window — reuse it for both candidates.
3. **Per-peer hysteresis.** After a direct-vs-relay swap, hold the
   choice for at least the comparison interval (5 min default,
   tunable via `TS_EXPERIMENTAL_DIRECT_VS_RELAY_HOLD=300s`). Prevents
   thrashing when both paths' latencies hover near the gate.
4. **Env knob.** `TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE=true`
   (off by default). Opt-in only; default behaviour is unchanged.
5. **New metrics.**
     - `magicsock_direct_vs_relay_compared`
     - `magicsock_direct_vs_relay_switched_to_relay`
     - `magicsock_direct_vs_relay_switched_to_direct`
     - `magicsock_direct_vs_relay_kept_direct_by_gate`
     - `magicsock_direct_vs_relay_kept_relay_by_gate`
   So operators can see whether the switching actually fires under
   load and tune the gate / hold timer.
6. **Operator visibility.** `tailscale debug paths` (or similar
   existing diagnostic) should display, for each peer: current
   path category, current latency, the alternative category's most
   recent measured latency, and the gate decision rationale.

### Open questions

- **Latency vs throughput.** A relay path can have lower RTT but
  much lower throughput (relay server CPU / bandwidth bottleneck).
  v1 of Phase 22 measures latency only; large file transfers might
  thrash if RTT-best ≠ throughput-best. Mitigations: only enable
  on peers explicitly tagged `low-latency-sensitive`, or measure
  loss-rate alongside latency and weight the score.
- **Loss-rate as a tiebreaker.** Existing send-failure counters
  (`metricSourcePathDataSendAuxFallback` and friends from Phase 19)
  give per-source-socket failure data. A v2 could include a
  `loss_penalty` term in the comparison.
- **Direct path's instability cost.** A "fresh" direct path sometimes
  has not-yet-warm RTT measurements (Phase 19 60-s mean smooths
  this, but the first 60 s after path establishment may be
  unrepresentative). Phase 22 should require the gate to have at
  least `sourcePathMinSamplesForUse = 3` valid samples on *both*
  the direct and relay sides before allowing a swap.
- **DERP fallback interplay.** When neither direct nor peer-relay
  is reachable, traffic is on DERP. Phase 22 doesn't change DERP
  behaviour; it only changes the direct-vs-peer-relay comparison.
  DERP remains the worst-case-but-always-available fallback.
- **Per-destination opt-out.** Some peers (e.g. file-server peers
  where throughput dominates) shouldn't get the comparison even
  when the global knob is on. A `TS_EXPERIMENTAL_DIRECT_VS_RELAY_OPT_OUT`
  env accepting NodeKeys could exclude them.
- **Polling vs event-driven trigger.** v1 reuses the existing
  `discoverUDPRelayPathsInterval` rate-limit and a longer 5-min
  comparison interval — i.e. periodic polling. The upstream TODO at
  `endpoint.go:933-939` already flags that periodic polling is the
  current architecture and that smarter triggers (inbound packets
  acting as discovery triggers, regular `CallMeMaybeVia` from the
  remote side, etc.) would need a coordination strategy that doesn't
  exist yet. v2 alternatives worth thinking about for Phase 22.x:
  trigger relay re-discovery when direct-path latency variance
  exceeds a threshold (e.g. `dlpv > primary.dlpv × 2` over the
  last 60 s), or piggy-back a "current direct latency" hint inside
  existing keep-alive frames so peers can opportunistically signal
  "you might want to re-probe me on a different path". v1 keeps it
  simple to bound implementation and review surface; v2 reduces
  steady-state probe overhead at the cost of more state machine.

### Out of scope for the candidate

- DERP-vs-peer-relay comparison. Phase 22 only handles
  direct↔peer-relay; DERP is treated as the unconditional
  fallback when neither is reachable.
- Active/active multi-path bonding. ZeroTier's `Bond.cpp`-style
  simultaneous send across both paths is a different feature.
  Phase 22 is path *selection*, not path *bonding*.
- Throughput-aware scoring. Latency only in v1.
- Multi-hop relay chains. Already out of scope generally; if the
  destination is reachable through `A → B1 → B2 → C` chained
  relays, that's a higher-order routing problem Tailscale doesn't
  have a primitive for.
- DSCP / QoS marking based on path choice.

### Estimated effort

- magicsock changes (`wantUDPRelayPathDiscoveryLocked` rewrite +
  cross-category gate + hysteresis + metrics): ~80 LoC Go.
- env knob plumbing + tests: ~50 LoC + ~120 LoC test.
- Phase doc + bilateral validation harness with deliberately
  latency-engineered topology (need to set up a topology where the
  relay is faster than direct — e.g. a VPS in the optimal-latency
  hop point relaying for two hosts whose direct path is BGP-detoured):
  ~250 LoC Python + 1 phase doc.
- Total: roughly the size of Phase 20, smaller than Phase 19.

### Why this matters operationally

For peers communicating over long-haul or cross-continental paths
where commercial peering arrangements introduce a non-optimal direct
route, Phase 22 lets Tailscale measure-and-pick instead of always
defaulting to direct. The case the operator described — "I have
peer relay nodes in different geographies; A↔C direct works but
goes through a slow path; some relay R has a much shorter total
A→R→C path" — is currently unaddressed by stock Tailscale and
would be addressed by this knob.

For the common case (direct is faster than any relay), Phase 22 has
a measurement cost (one relay-discovery cycle every 5 min instead
of "never") but no swap, so the steady-state direct-on-direct path
is identical to today.

---

## Phase 23 (candidate) — Active/active multi-source dual-send

**Problem.** srcsel today is a single-source-per-send model. For each
WireGuard datagram, `endpoint.send` (`wgengine/magicsock/endpoint.go:
1130-1148`) calls `c.sourcePathDataSendSource(udpAddr)` to pick *one*
source — primary or one of the auxiliary sockets — and writes the packet
through that single socket via `sendUDPBatchFromSource`
(`magicsock.go:1539-1547`). On aux send failure, the same call site
falls back to primary and invalidates the aux samples
(`noteSourcePathSendFailure`).

This is the right default for steady-state traffic, but it leaves no
in-flight redundancy. A single packet lost on the chosen path is just
lost; the WireGuard / app layer above must detect and recover. For
real-time workloads (interactive voice, gaming, latency-sensitive
control planes), recovery latency at hundreds of milliseconds is too
slow — users perceive the gap. Phase 19/20 scorer-driven switching
reacts on a 10-15 second timescale (Phase 19's `sourcePathMinSamplesForUse=3`
× Phase 20's disco-coupled probe cadence ~5 s); it cannot mask
single-packet loss.

**Why this is *not* covered by Phase 1-22.** Phase 19 enabled
*receiving* WireGuard data on aux sockets, but the sender still writes
each packet to exactly one source. Phase 20 added scorer-based aux vs
primary selection, but it is a switchover not a fan-out: only one
source carries the live data plane at any moment. Phase 22 added
direct-vs-relay comparison, but again on a single chosen path. None of
these introduce per-packet redundancy across sources.

**Why this is *not* the same as DERP fallback.** DERP is the
worst-case-but-always-available reachability fallback when no UDP
path works. Phase 23 adds redundancy *while* UDP is working — primary
and aux both UDP, both delivering the same payload, replay window on
the receiver dropping the duplicate.

**Reference design — ZeroTier `BOND_POLICY_BROADCAST`.** ZeroTier's
multipath layer implements this exact pattern in
`node/Switch.cpp:1061-1077`:

```cpp
if (peer->bondingPolicy() == ZT_BOND_POLICY_BROADCAST &&
    (packet.verb() == VERB_FRAME || packet.verb() == VERB_EXT_FRAME)) {
    for (int i = 0; i < ZT_MAX_PEER_NETWORK_PATHS; ++i) {
        if (peer->_paths[i].p && peer->_paths[i].p->alive(now)) {
            _sendViaSpecificPath(...);  // same packet on every alive path
        }
    }
}
```

ZeroTier relies on its VL1 packet-id replay window for receiver-side
dedup. WireGuard already provides the equivalent: an 8128-counter
sliding window per peer implementing RFC 6479 (`replay/replay.go`,
consumed via `replay.Filter` in `device/keypair.go`).
A duplicate WireGuard packet arriving on aux after the same packet on
primary is silently rejected by the WG state machine without
delivering twice to the inner network stack. The protocol-layer
prerequisite for dual-send already holds.

### Sketch

1. **Single env knob** `TS_EXPERIMENTAL_SRCSEL_DUAL_SEND=true` (off
   by default). With the knob unset, `endpoint.send` is bit-identical
   to today (Phase 19/20 single-source). With it on AND aux is bound
   AND the dst is direct-UDP AND the existing scorer permits aux
   (`sourcePathBestCandidate(dst)` returns ok), perform both sends:
     - Primary first via `sendUDPBatch(udpAddr, buffs, offset)` —
       error handling identical to today (`noteBadEndpoint` on
       `isBadEndpointErr`).
     - Aux second via `sendUDPBatchFromSource(auxSource, ...)` —
       failure does **not** trigger `noteSourcePathSendFailure`. That
       call (`sourcepath.go:235-245`) drops samples for the failed
       (dst, source) pair, which is correct for selection-mode
       failures but pessimistic for dual-send: one transient aux ENETUNREACH
       would dump the entire sample buffer and force re-accumulation.
       Track aux-side dual-send failures in a separate per-(dst, source)
       streak counter instead.
2. **Aux-fail-streak fallback.** Track `dualSendAuxFailStreak[dst]`.
   On `aux_fail_streak >= TS_EXPERIMENTAL_SRCSEL_DUAL_SEND_AUX_DROP_STREAK`
   (default 5), demote that dst to single-send for
   `TS_EXPERIMENTAL_SRCSEL_DUAL_SEND_RECOVERY_S` seconds (default 30),
   then re-enable. During the demotion window, aux probes still run —
   the path is observed for selection but not used for fan-out.
3. **Primary-fail interaction.** If primary send fails AND aux send
   succeeded in the same dual-send call, the call as a whole counts
   as delivered (the receiver does not care which copy arrived first).
   Skip `noteBadEndpoint` for primary in that case to avoid bestAddr
   churn — the path is salvageable via aux while primary's failure
   reason resolves. Surface the asymmetry via a metric so operators
   can see "primary leg of dual-send is silently failing" without
   waiting for aux to also fail. (Phase 25 Active/Backup failover
   subsumes the harder case where primary is fully dead and aux must
   carry alone.)
4. **Receive side: nothing new.** Phase 19's `receiveIPWithSource`
   already delivers aux-arrived data into the WireGuard inbound queue
   (`magicsock.go` aux receive func wires through `mkReceiveFuncWithSource`).
   The replay window in `wireguard-go/replay/replay.go` (consumed via
   `replay.Filter` in `device/keypair.go`) handles dedup transparently.
   No new RX code needed.
5. **Mid-flight reordering bound.** Aux and primary may have different
   one-way latency. WireGuard's replay filter
   (`replay/replay.go`, RFC 6479 sliding window) has a fixed depth of
   8128 counters (`(ringBlocks-1) * blockBits` with `ringBlocks=128`,
   `blockBits=64`). When primary's copy of packet N is delivered first,
   it advances `f.last` to N. Aux's copy of N arriving later — whether
   within the window (rejected as duplicate, ring bit already set) or
   beyond the window (rejected as too-old via
   `f.last-counter > windowSize`) — is silently dropped without ever
   reaching the inner stack. Reordering between primary and aux is
   therefore safe at any skew. The real concern is *redundancy value*:
   aux only contributes when primary's copy is lost AND aux's copy
   lands within window of primary's current `f.last`. If
   `|aux.latency - primary.latency| × packet_rate > 8128`, aux's copies
   are systematically beyond the window and provide zero loss-recovery
   benefit while still consuming upstream bandwidth. Mitigation: gate
   dual-send on `|aux.latency - primary.latency| <
   TS_EXPERIMENTAL_SRCSEL_DUAL_SEND_MAX_SKEW_MS` (default 100 ms). At
   1000 packets/sec and 100 ms skew, aux is ~100 packets behind —
   safely within the 8128 window. With Phase 20 scorer running, the
   latency comparison data is already available without new probing.
6. **New metrics.**
     - `magicsock_srcsel_dual_send_packets`
     - `magicsock_srcsel_dual_send_primary_failed`
     - `magicsock_srcsel_dual_send_aux_failed`
     - `magicsock_srcsel_dual_send_both_failed`
     - `magicsock_srcsel_dual_send_demoted_aux_streak`
     - `magicsock_srcsel_dual_send_skipped_skew`
   Operators can use the ratio
   `dual_send_aux_failed / dual_send_packets` to see the redundancy
   yield: how often the redundancy actually mattered (primary fail
   + aux delivered).
7. **Bandwidth doubling.** Document that dual-send doubles upstream
   bytes for direct-UDP traffic to that peer. The trade is explicit
   and behind the env knob. For a 1 Mbps gaming session, doubling to
   2 Mbps is well within typical home uplinks; for a 100 Mbps file
   transfer, it is not. Operators can opt in per deployment.

### Open questions

- **Replay window depth vs. burst loss.** The 8128-counter window is
  large but not unlimited. If a flow is in a burst-loss regime where
  primary loses 1000 consecutive packets before recovery, aux's
  twin packets would arrive long after the receiver expected them
  and the sequence numbers would already be ahead. WireGuard would
  reject them as too-old. v1 accepts this loss case as identical to
  single-send; mitigation in v2 could be a small per-path keepalive
  pad so the window does not slide too far before aux catches up.
- **Bidirectional dual-send symmetry.** Sender chooses dual-send;
  receiver does not need to know. But for *bidirectional* redundancy
  (both directions of the WG session masked), both peers must enable
  the knob. Document the sender-only case as upstream-only redundancy
  and the bilateral case as full redundancy. Phase 23 v1 ships only
  the sender side; the receiver-side switch (advertising support so
  the peer enables their direction) is left to v2 if the upstream
  case proves valuable.
- **Asymmetric NAT (W7 row 1) interaction.** Bilateral NAT means
  aux reverse hole-punching may fail (W7 evidence: aux RX path
  structurally unreached because peer never sends to aux src port).
  For dual-send, the SENDER drives the NAT punch — own-side NAT
  only — so the upstream half works in W7 case. Downstream
  redundancy needs both peers to enable dual-send AND a topology
  where aux RX is reachable (Phase 19/W10 evidence: dual public IPs
  needed for symmetric aux↔aux). Phase 23 ships honest about this:
  "single-side dual-send" is fully supported; "bilateral dual-send"
  is supported but its receiver-side benefit is gated on topology.
- **Probe gate strictness.** Today's `sourcePathMinSamplesForUse=3`
  + 10 % beat threshold gate aux selection (Phase 19/20). For
  dual-send these gates are strictly conservative — duplicate packets
  on a "bad" aux waste bandwidth but do not corrupt anything. v1
  keeps the existing gate (only fan-out when scorer says aux is
  worth it). v2 could relax to "aux bound and recently-pong" without
  the 10 % threshold, accepting more bandwidth in exchange for more
  redundancy coverage.
- **Interaction with Phase 24 multi-metric scorer.** When Phase 24
  ships, the dual-send "should I also send on aux" decision should
  use the multi-metric score (jitter/loss-aware), not the Phase 20
  mean-RTT-only score. Phase 23 v1 lands first with Phase 20 gating;
  the gate-source field is internal and can be swapped without
  changing the dual-send semantics.

### Out of scope for the candidate

- Triple-send across primary + aux4 + aux6 (only one aux family
  per direct-UDP destination today; per-family dual-send requires
  the dst to be reachable on multiple AFs, which is the same
  topology Phase 26 might address).
- Selective dual-send by packet size or DSCP marking.
- Application-layer signalling (game protocol asking the OS for
  redundancy on specific frames).
- Triple-redundancy via a relay path in addition to direct
  (overlaps with Phase 22; out of scope for v1).

### Estimated effort

- magicsock changes (`endpoint.send` fan-out branch + per-dst
  fail-streak tracking + skew gate + metrics): ~120 LoC Go.
- Env knob plumbing + tests: ~60 LoC + ~180 LoC test (replay-window
  dedup verification on dst with primary+aux both bound).
- Phase doc + bilateral validation harness on the dual-public-IP
  Pair 2 topology already established in W10 (`216.144.236.235 ↔
  64.64.254.58`), with `tc qdisc netem` injected loss on primary to
  verify aux redundancy actually masks it: ~300 LoC Python + 1
  phase doc.
- Total: similar size to Phase 19, smaller than Phase 8.

---

## Phase 24 (candidate) — Multi-metric scorer with jitter and loss

**Problem.** The Phase 20 scorer (`wgengine/magicsock/sourcepath.go:
281-344`) compares an aux candidate to primary by *mean latency only*.
The current decision rule is:

```go
candidate.latency = mean(latencySamples)         // mean RTT
if primaryRTT > 0 && thresholdPct > 0 {
    cutoff := primaryRTT - primaryRTT*pct/100    // 10 % beat threshold
    if candidate.latency >= cutoff { reject }    // not "much" faster
}
```

This misses the dimensions real-time workloads care about most:

- **Jitter** (latency variance / packet delay variance / PDV). A
  100 ms-mean / 50 ms-stddev path is unusable for voice and games;
  a 110 ms-mean / 5 ms-stddev path is excellent. The current scorer
  cannot distinguish: both have similar means.
- **Packet loss.** srcsel today has no view of loss rate. Probe
  expirations are silently dropped from the `pending` map after
  `pingTimeoutDuration` (`sourcepath.go:364-375`), with the count
  emitted as a debug metric (`metricSourcePathProbePendingExpired`)
  but not fed into scoring. A path with 90 ms mean + 10 % loss
  beats a path with 95 ms mean + 0 % loss in today's scorer; for
  real-time traffic that is exactly backwards.
- **Probe cadence.** Probes ride disco-ping cadence (`endpoint.go:
  1421-1424` calls `sendSourcePathDiscoPing` inline with
  `startDiscoPingLocked`, rate-limited at `discoPingInterval = 5 s`
  per disco call site). Three samples — the Phase 19
  `sourcePathMinSamplesForUse=3` minimum — therefore takes 10-15 s
  in the best case. Mobile / wifi networks change quality on
  second-scale; the scorer cannot react.

Phase 22's open question already flagged direction (2): "Loss-rate as
a tiebreaker. ... A v2 could include a `loss_penalty` term in the
comparison." Phase 24 picks that up and generalizes it.

**Why this is *not* covered by anything already shipped.** Phase 4 /
4b laid the scorer plumbing (observation buffer, mean-latency
selection); Phase 8 added safety budgets; Phase 19 added the
3-sample / 60 s TTL / send-failure invalidation safety gates;
Phase 20 added the primary-baseline 10 % beat threshold. None
introduce additional dimensions to the scoring function or change
the probe cadence — they all operate on the mean-RTT-only signal.

**Reference design — ZeroTier `Bond.cpp`.** ZeroTier's multi-path
quality model (`node/Bond.cpp:1290-1351`) provides four raw metrics
per path:

```cpp
lat[i] = 1.0 / expf(4 * normalize(_paths[i].latency, 0, _qw[ZT_QOS_LAT_MAX_IDX], 0, 1));
pdv[i] = 1.0 / expf(4 * normalize(_paths[i].latencyVariance, 0, _qw[ZT_QOS_PDV_MAX_IDX], 0, 1));
plr[i] = 1.0 / expf(4 * normalize(_paths[i].packetLossRatio, 0, _qw[ZT_QOS_PLR_MAX_IDX], 0, 1));
per[i] = 1.0 / expf(4 * normalize(_paths[i].packetErrorRatio, 0, _qw[ZT_QOS_PER_MAX_IDX], 0, 1));

absoluteQuality[i] += (lat[i] / maxLAT) * _qw[ZT_QOS_LAT_WEIGHT_IDX];
absoluteQuality[i] += (pdv[i] / maxPDV) * _qw[ZT_QOS_PDV_WEIGHT_IDX];
absoluteQuality[i] += (plr[i] / maxPLR) * _qw[ZT_QOS_PLR_WEIGHT_IDX];
absoluteQuality[i] += (per[i] / maxPER) * _qw[ZT_QOS_PER_WEIGHT_IDX];
```

Plus a hard-avoid layer (`Bond.cpp:1380-1410`): any single metric
exceeding its max disables the path before scoring even runs.

### Sketch

1. **Probe cadence decoupling.** Add a dedicated source-path probe
   ticker on `Conn`, independent of disco ping cadence:
     - Default 1000 ms (`TS_EXPERIMENTAL_SRCSEL_PROBE_INTERVAL_MS`)
     - Floor 200 ms (matches ZeroTier's `ZT_BOND_FAILOVER_MIN_INTERVAL=500`
       halved for two-way RTT). Below 200 ms, probe traffic begins
       to interfere with measurement itself.
     - Off-by-default semantics: knob unset retains today's
       disco-ping-coupled cadence so Phase 20 deployments see no
       behaviour change.
     - Per-peer rate-limit: existing
       `sourcePathProbeMaxBurstCount()=8` (Phase 8) caps in-flight
       probe count; the cadence change does not bypass it.
2. **Jitter dimension.** `sourcePathProbeManager.samples[]` already
   carries per-sample latency + timestamp (`sourcepath.go:126-134`).
   Compute population stddev in `bestCandidateLocked`:
   ```go
   var sumNs, sumSqNs int64
   var n int64
   for _, s := range freshSamples {
       sumNs += s.latency.Nanoseconds()
       sumSqNs += s.latency.Nanoseconds() * s.latency.Nanoseconds()
       n++
   }
   meanNs := sumNs / n
   varianceNs := sumSqNs/n - meanNs*meanNs
   jitter := time.Duration(int64(math.Sqrt(float64(varianceNs))))
   ```
   Add `jitter` to `sourcePathCandidateScore` alongside `latency`.
3. **Loss dimension.** Track per-(dst, source) probe outcomes over a
   sliding window in a new `sourcePathLossTracker`:
     - On probe send: increment `tx_count`
     - On pong-accepted: increment `rx_count`
     - On `pruneExpiredLocked` deletion (txid expired without pong):
       increment `loss_count`
     - Loss ratio = `loss_count / tx_count` over last
       `TS_EXPERIMENTAL_SRCSEL_LOSS_WINDOW_S` seconds (default 30 s)
     - Emit `magicsock_srcsel_path_loss_ratio_*` family of metrics
       per-source for observability.
4. **Multi-metric scoring formula.** Mirrors ZT's normalized +
   weighted approach. Inside `bestCandidateLocked`, when
   `TS_EXPERIMENTAL_SRCSEL_MULTI_METRIC=true`, replace the single
   "mean < primary × 0.9" gate with:
   ```
   latNorm    = 1.0 / expf(4 * clamp(cand.latency / latMax, 0, 1))
   jitterNorm = 1.0 / expf(4 * clamp(cand.jitter  / jitterMax, 0, 1))
   lossNorm   = 1.0 / expf(4 * clamp(cand.loss    / lossMax, 0, 1))
   score      = latNorm * latW + jitterNorm * jitterW + lossNorm * lossW
   ```
   Defaults aim for real-time / interactive workloads:
     - `latW=0.30, jitterW=0.40, lossW=0.30` (jitter weighted highest)
   Operator-tunable via comma-separated env:
     - `TS_EXPERIMENTAL_SRCSEL_SCORE_WEIGHTS=lat=0.3,jit=0.4,loss=0.3`
5. **Hard-avoid layer.** Per-metric absolute maxes; any candidate
   exceeding any max is excluded *before* scoring runs. Mirrors ZT's
   `shouldAvoid`:
     - `TS_EXPERIMENTAL_SRCSEL_LATENCY_MAX_MS=300` (default)
     - `TS_EXPERIMENTAL_SRCSEL_JITTER_MAX_MS=50`
     - `TS_EXPERIMENTAL_SRCSEL_LOSS_MAX_PCT=5`
   New reject metrics:
     - `magicsock_srcsel_hard_avoid_latency`
     - `magicsock_srcsel_hard_avoid_jitter`
     - `magicsock_srcsel_hard_avoid_loss`
6. **Aux vs primary comparison rule.** Phase 20's "aux mean <
   primary RTT × 0.9" cannot apply directly to the multi-metric
   score because primary doesn't have jitter / loss measured
   (probes go through aux). Until primary instrumentation is
   added (open question below), v1 compares aux's full multi-metric
   score against a primary-derived score that uses only the latency
   dimension (other dimensions assumed perfect for primary). aux
   wins iff `aux.score > primary.score * (1 + improvement_pct/100)`,
   default `improvement_pct=5`.
7. **Profile presets.** Convenience knob bundling defaults:
     - `TS_EXPERIMENTAL_SRCSEL_PROFILE=server` (current Phase 20:
       5 s probe, 60 s TTL, mean-only)
     - `TS_EXPERIMENTAL_SRCSEL_PROFILE=realtime` (1 s probe, 10 s
       TTL, multi-metric, jitter-weighted)
   Individual knobs still override the profile.
8. **TTL tightening.** Faster cadence implies a tighter freshness
   window. Suggest `effectiveTTL = max(60s, probeInterval × 30)`,
   tunable via `TS_EXPERIMENTAL_SRCSEL_SAMPLE_TTL_S`. Smaller TTL
   = faster reaction, less smoothing — exposed as an explicit
   trade-off.
9. **Backwards compat.** `TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT`
   (Phase 20) remains the pure-mean-RTT gate. Multi-metric scoring
   is opt-in via `TS_EXPERIMENTAL_SRCSEL_MULTI_METRIC=true`. With
   it off (default), Phase 20 behaviour is bit-identical.

### Open questions

- **Primary-path instrumentation.** The biggest gap. Today, primary
  RTT is observed via DISCO ping pong (existing
  `endpoint.disco{Ping,Pong}` plumbing). That gives us mean RTT
  but neither jitter nor loss. To compare primary fairly under the
  multi-metric scorer, primary needs the same probe cadence and
  loss/jitter accounting. Sketch: extend `endpoint.startDiscoPingLocked`
  call sites to also feed the new loss tracker for primary, and use
  the same sliding window. v1 may ship with "primary loss/jitter
  unknown — assumed perfect"; v2 closes this gap.
- **Probe overhead at 1 Hz.** 1 probe / sec / peer / source × <100 B
  = ~100 B/s/peer/source. Phase 19 lifted the per-peer cap (default
  unlimited) and the hard pending cap (100 K), so a 1000-peer
  server runs ~100 KB/s probe traffic — acceptable but worth
  documenting. Per-peer cadence override would let large
  fleets dial down for low-priority peers; v1 ships a single
  global value.
- **Loss-window granularity.** 30 s window × 1 Hz = 30 samples;
  statistically thin for low-loss links. 1 % loss = 0.3 events per
  window. Mitigation: report-only with `low_confidence=true` flag
  below 100 samples; treat `lossNorm` as 1.0 (perfect) until enough
  data accumulates.
- **Score discontinuity vs. Phase 20 binary gate.** Phase 20's gate
  is binary (aux either passes or doesn't); multi-metric is
  continuous. The 5 % improvement threshold preserves the spirit
  of Phase 20 (don't switch for trivial improvements). Larger
  thresholds reduce thrashing; smaller ones increase responsiveness.
  Default tuning will need empirical work on the bilateral
  validation harness.
- **Interaction with Phase 23 dual-send.** Phase 23 uses the
  scorer's "is aux usable" signal to decide whether to fan out.
  If Phase 24 lands first, Phase 23's gate is the multi-metric
  score; if Phase 23 lands first, the gate is Phase 20's mean-only.
  Either order works; the interface between scorer and consumer
  (Phase 23 fan-out, Phase 25 failover) is the boolean
  "is aux currently a viable target". Document the ordering once
  shipping order is decided.
- **Pong-arrival jitter ≠ network jitter.** Computing jitter from
  sampled probe RTTs measures the round-trip jitter, including
  remote-side processing variance. For most cases that is what we
  want; for high-precision use cases the upstream/downstream split
  would matter. Out of scope for v1.

### Out of scope for the candidate

- Active throughput probing (only passive / probe-derived
  measurements).
- Per-flow scoring (Phase 26 territory).
- ML-based path quality prediction.
- Receiver-side score export to upstream (the scorer is sender-only).
- Cross-NAT path quality fingerprinting / blocklists.

### Estimated effort

- magicsock runtime (probe ticker + jitter compute + loss tracker
  + scoring formula + hard-avoid layer + profile presets): ~280 LoC
  Go.
- Env knob plumbing (~10 new knobs) + tests: ~80 LoC + ~250 LoC test.
- Phase doc + bilateral validation harness with `tc qdisc netem`
  injecting jitter and loss on a primary path while aux remains
  clean, verifying the scorer correctly steers traffic to aux:
  ~400 LoC Python + 1 phase doc + 1 lossy-link harness script.
- Total: largest of the four candidates here; between Phase 8 and
  Phase 19 in size.

---

## Phase 25 (candidate) — Active/backup primary failover

**Problem.** srcsel today only switches *toward* aux when the scorer
finds aux faster than primary. There is no path for switching *away
from* a primary that is actively failing. Concretely:

- `sourcePathDataSendSource` (`sourcepath_supported.go:166-184`)
  defaults to primary; only returns aux when the scorer agrees aux
  beats primary by the Phase 20 threshold.
- `bestCandidateLocked` compares aux mean RTT against primary RTT
  using `primaryRTTForLocked(dst)`. If primary RTT goes stale (no
  recent pong), the comparison still uses the last observed value —
  a dead primary "looks fast" until the heartbeat-driven re-discovery
  kicks in much later.
- Aux send failure is handled (Phase 19: `noteSourcePathSendFailure`
  invalidates aux samples). Primary send failure does NOT trigger
  any srcsel-side switch — it just propagates to `noteBadEndpoint`
  (`endpoint.go:1149-1154`), which clears `bestAddr` and triggers
  re-discovery. That path is several seconds slow and goes through
  DERP fallback before any aux is reconsidered.

Net effect: a "primary path silently broken" — NAT remap, route
flap, ISP brownout — takes `trustUDPAddrDuration=6500 ms` plus
DERP-fallback latency to recover, even when a perfectly healthy aux
is bound and probe-active right next to it. Real-time apps see
hundreds of milliseconds of stalls; automation pipelines see request
timeouts.

**Why this is *not* covered by anything already shipped.** Phase 19
addressed *send failure on aux*, not *send failure on primary*.
Phase 20 added a comparator favouring better paths but never an
escape hatch for a failing primary. Phase 22 added direct-vs-relay
comparison but does not consider aux as a same-category alternative
to a degraded direct.

**Reference design — ZeroTier `BOND_POLICY_ACTIVE_BACKUP`.**
ZeroTier's multipath layer (`node/Bond.cpp:370-375` and
`processActiveBackupTasks`) provides exactly this. A primary path is
chosen; aux paths sit warm; when primary's `_failoverInterval=5 s`
elapses without confirmed liveness (no inbound traffic AND ECHO
heartbeat unanswered), an aux is promoted. ZT's failover floor is
500 ms (`ZT_BOND_FAILOVER_MIN_INTERVAL`) — sub-second swap is the
intent.

### Sketch

1. **Primary liveness tracker.** Add a `primaryHealth` struct on
   `Conn` keyed by (peer, primary-addr):
   ```go
   type primaryHealth struct {
       lastSendOK     mono.Time   // last successful sendUDPBatch
       lastRecvAny    mono.Time   // last recv from peer via primary
       sendFailStreak int         // consecutive primary send failures
   }
   ```
   Updated by:
     - `sendUDPBatchFromSource(primary, ...)` success / failure
     - `receiveIPWithSource` when source == primary
     - DISCO pong receipt on primary
2. **Primary-unhealthy criteria.** Any of the following trips the
   forced-failover state:
     - `sendFailStreak >= TS_EXPERIMENTAL_SRCSEL_PRIMARY_FAIL_STREAK`
       (default 3 consecutive failures)
     - `now.Sub(primary.lastRecvAny) > TS_EXPERIMENTAL_SRCSEL_PRIMARY_SILENCE_S`
       (default 2 s, only triggers if probes are running — see
       open question on Phase 24 dependency)
     - Most recent disco pong RTT timed out via existing
       `pingTimeoutDuration` machinery
   When tripped, `sourcePathDataSendSource(dst)` returns aux
   *unconditionally* — bypassing the Phase 20 beat-threshold gate.
   The semantic shift is "aux is now mandatory" rather than "aux is
   permitted".
3. **Failover hold + recovery.**
     - After a forced switch, hold the aux choice for at least
       `TS_EXPERIMENTAL_SRCSEL_FAILOVER_HOLD_S` (default 30 s) to
       avoid thrashing.
     - During the hold, continue DISCO probes through primary so
       its recovery is observable.
     - To exit forced-aux mode, require either (a) the hold expired
       AND most recent primary pong succeeded, or (b) a longer
       `TS_EXPERIMENTAL_SRCSEL_FAILOVER_RECOVERY_PONGS` (default 3)
       consecutive pongs on primary regardless of hold.
4. **Aux-also-dead fallback.** If forced to aux but aux is also
   failing (aux send failures + no aux pong for the same window),
   fall through to DERP via the existing `endpoint.send` derpAddr
   path. No new code beyond ensuring the failover state propagates
   to "give up on direct UDP" rather than oscillating between
   primary and aux.
5. **Sequencing with `noteBadEndpoint`.** Today, primary send
   failure clears `bestAddr` and triggers re-discovery
   (`endpoint.go:1149-1154`). Phase 25's failover-to-aux must
   happen BEFORE `noteBadEndpoint` clears bestAddr — otherwise
   the destination address itself is gone and aux has nowhere to
   send. Order:
     - Primary send fails → bump `sendFailStreak`.
     - If streak crosses threshold, mark forced-failover.
     - Retry send via aux on the same `endpoint.send` invocation
       (parallels the existing aux-fail-then-primary retry at
       `endpoint.go:1138-1144`, but reversed direction).
     - Only call `noteBadEndpoint` when both primary AND aux fail
       on the retry.
6. **Env knob.** `TS_EXPERIMENTAL_SRCSEL_ACTIVE_BACKUP=true` (off
   by default). When off, today's behaviour is bit-identical.
7. **New metrics.**
     - `magicsock_srcsel_primary_unhealthy_send_streak`
     - `magicsock_srcsel_primary_unhealthy_silence`
     - `magicsock_srcsel_primary_unhealthy_pong_timeout`
     - `magicsock_srcsel_failover_to_aux`
     - `magicsock_srcsel_failover_recovered_to_primary`
     - `magicsock_srcsel_failover_aux_also_dead`

### Open questions

- **Phase 24 dependency for accurate liveness.** "Silence > 2 s"
  is meaningful only if there is regular traffic on primary. For
  idle peers, normal traffic gaps trip the failover. The Phase 24
  1 Hz independent probe ticker resolves this: silence is measured
  against probe-pong responses, not against application traffic.
  Phase 25 is therefore most useful *after* Phase 24 ships. v1
  could ship with the silence trigger gated on "probes are
  currently running on primary" and otherwise rely on
  send-fail-streak only — a less sensitive but still useful
  failover.
- **Recovery hysteresis.** A single recovered pong followed by 5
  more failures is fragile. The `RECOVERY_PONGS=3` requirement
  plus the hold timer mitigates but does not eliminate the case.
  If thrashing is observed in validation, weighted moving average
  of recent pong success rate (e.g. require 80 % over the last
  30 s) is a v2 refinement.
- **Phase 22 direct-vs-relay intersection.** If primary is
  unhealthy AND aux is unhealthy AND DERP is reachable, DERP wins
  immediately — but Phase 22's per-peer `DIRECT_VS_RELAY_HOLD_S`
  (default 300 s) timer should not delay this. Phase 25's "give
  up on direct" must short-circuit Phase 22's hold for the
  failover case. Implementation: a "force-relay-now" flag set by
  Phase 25 that Phase 22's `betterAddr` consults.
- **Asymmetric NAT (W7) behaviour.** Bilateral NAT means aux
  reverse hole-punching may fail (W7 evidence). If primary dies
  AND the topology is bilateral NAT, aux may not have a working
  downlink. Force-switching to aux still gives upstream
  redundancy but downstream is broken. Phase 25 reports
  `failover_to_aux` succeeded (the send went through) but real
  bidirectional recovery requires aux RX to be reachable — only
  guaranteed in dual-public-IP / single-side-NAT topologies.
  Document this honestly: "Phase 25 + bilateral NAT =
  upstream-only failover; full failover needs Phase 21 dynamic
  endpoint set or a peer with a public IP".
- **Coordination with Phase 23 dual-send.** Phase 23 already
  delivers via aux *while* primary is healthy. Phase 25 escalates
  by treating primary as gone. The state transitions:
    - Phase 23 active, primary OK, aux OK → both sends, normal
    - Phase 23 active, primary failing, aux OK → Phase 25 promotes,
      aux carries alone, Phase 23 effectively becomes single-send-via-aux
    - Phase 23 active, primary OK, aux failing → Phase 23
      single-send via primary, Phase 25 unaffected
  No deadlock; the two phases compose. Order: Phase 23 first
  (simpler), Phase 25 second (depends on Phase 23's aux-RX path
  being reliably wired).

### Out of scope for the candidate

- DERP-as-primary cases (Phase 22 owns direct-vs-relay).
- Multi-aux failover (aux4 ↔ aux6 cross-family failover; v1 only
  does primary↔single-aux).
- Predictive failover from jitter spikes / loss bursts (Phase 24
  scorer territory).
- Coordinated peer-side awareness (the failed peer does not learn
  that we failed over; it sees aux source address change and
  reacts via existing `lazyEndpoint` plumbing).

### Estimated effort

- magicsock runtime (primary health tracker + failover decision +
  recovery state machine + sequencing rewrite around
  `noteBadEndpoint`): ~140 LoC Go.
- Env knobs (~5 new) + tests: ~50 LoC + ~180 LoC test (state
  machine coverage matrix: healthy / send-fail / silence /
  recovery / both-dead).
- Phase doc + bilateral validation harness with `iptables -j DROP`
  injected to simulate primary dying, watching `failover_to_aux`
  trip + `failover_recovered_to_primary` resolve when DROP
  removed: ~280 LoC Python + 1 phase doc.
- Total: similar size to Phase 20 + W10 combined; smaller than
  Phase 24.

---

## Phase 26 (candidate, deferred) — Flow-aware source selection

**Problem.** Multi-flow workloads — a peer carrying browser + voice
+ game + file transfer over the same WireGuard tunnel — would
benefit from sending different flows on different sources: bulk
file transfer to aux (offload primary), voice to primary (lowest
jitter), interactive to whichever scorer says is best per-flow.
srcsel today has no flow concept. `sourcePathDataSendSource(dst)`
returns one source per (peer, dst); all flows to the same peer go
through the same source.

**Why this is *not* covered by Phase 23.** Phase 23 (dual-send) is
*per-packet*, not per-flow: every packet goes on every path. That
gives redundancy but doubles bandwidth. Phase 26 splits flows
across paths so each path carries roughly half the bandwidth — a
different point on the redundancy / utilisation tradeoff.

**Reference design — ZeroTier `BOND_POLICY_BALANCE_AWARE`.**
ZeroTier's flow-aware policy (`node/Bond.cpp:422-441` for
selection, `535-547` for flow learning):

```cpp
if (_policy == ZT_BOND_POLICY_BALANCE_AWARE) {
    auto it = _flows.find(flowId);
    if (it != _flows.end()) {
        return _paths[it->second->assignedPath].p;  // sticky
    }
    // First sight: pick a path weighted by relativeQuality
    SharedPtr<Flow> flow = createFlow(...);
    _flows[flowId] = flow;
    return _paths[flow->assignedPath].p;
}
```

A 5-tuple flow ID is hashed; first packet of a flow is assigned a
path weighted by per-path quality score; subsequent packets stick.
Stickiness avoids per-packet reordering (essential for TCP) while
still distributing flows across paths.

### Sketch

1. **Flow identification.** magicsock works on encrypted blobs, so
   a 5-tuple flow ID is not directly observable. The flow hint
   must come from the layer above:
     - Option A: extend `wireguard-go`'s `conn.Bind.Send` interface
       to optionally pass a flow hint (the userspace
       wireguard-go bind has the inner 5-tuple before encryption).
       This is an upstream-touching change.
     - Option B: hash the encrypted ciphertext's first N bytes
       (which encode the WG handshake / data-packet sequence
       fingerprint). Stable per-key but not per-inner-flow; degenerates
       to "all flows hash the same" for a single peer-key.
     - Option C: do nothing magicsock-side; ship the policy as
       ZT-style `BALANCE_RR` (round-robin every N packets per
       path). Loses stickiness benefit but avoids the upstream
       interface change.
   Option A is the right design; Option C is the cheap fallback.
2. **Flow → source mapping.** New `flowMap` per peer:
   ```
   flowID -> (assignedSource, lastActivity)
   ```
     - On flow first-seen: assign weighted by Phase 24 score
     - On subsequent packets: stick to assigned
     - On flow timeout (no traffic for `TS_EXPERIMENTAL_SRCSEL_FLOW_IDLE_S`,
       default 30 s): release the assignment
3. **Allocation strategies.**
     - `BALANCE_RR`: stripe per-flow-equivalent batch across paths
     - `BALANCE_XOR`: deterministic flowHash → path
     - `BALANCE_AWARE`: weighted by Phase 24 score
   Selectable via `TS_EXPERIMENTAL_SRCSEL_BALANCE_POLICY=rr|xor|aware`.
4. **Backwards compat.** `TS_EXPERIMENTAL_SRCSEL_FLOW_AWARE=true`
   (off by default). When off, `sourcePathDataSendSource` is
   unchanged.

### Open questions

- **Flow hint plumbing.** This is the load-bearing open question.
  Without an upstream wireguard-go interface change (Option A),
  the candidate degrades to either Option B (hash that doesn't
  separate flows) or Option C (per-packet policy without
  stickiness). Either makes the candidate strictly less valuable
  than Phase 23 + Phase 24 + Phase 25 combined. Recommendation:
  defer Phase 26 until concrete user demand surfaces or until
  upstream wireguard-go gains a flow-hint extension for unrelated
  reasons.
- **Asymmetric flow visibility.** Sender sees its own flow ID;
  receiver sees only the WG packet. Receiver-side source selection
  cannot use flow-aware logic; it remains per-(peer, dst).
  Document that flow-aware is upstream-only.
- **Flow churn.** Short-lived flows (DNS lookups, single-shot
  HTTP) churn the flow map quickly. Cap `flowMap` size + LRU
  eviction; reuse the Phase 8 budget machinery.
- **Interaction with Phase 23 dual-send.** Phase 23 fan-outs each
  packet across all paths; flow-aware (Phase 26) sends each flow
  on one path. They are not orthogonal — flow-aware overrides
  dual-send for assigned flows. Document precedence: when both
  knobs are on, dual-send wins (redundancy beats utilisation).
- **Real-world demand.** Most Tailscale workloads are single-flow
  per peer (terminal session, file mirror, single browser tab,
  single game session). The multi-flow benefit is real but
  narrow. **This candidate is deferred** until concrete
  demand exists.

### Out of scope for the candidate

- magicsock-side 5-tuple inspection (would require decrypting WG,
  breaking trust boundary).
- Receiver-side flow steering.
- DSCP / QoS marking based on flow class.
- Per-flow MTU adjustment.

### Estimated effort

- Runtime (flow map + allocation policy + integration): ~220 LoC
  Go, plus ~120 LoC if the upstream wireguard-go bind interface
  extension is needed.
- Env knobs + tests: ~70 LoC + ~250 LoC test.
- Phase doc + multi-flow validation harness: ~350 LoC Python + 1
  phase doc.
- Total: similar to Phase 19; this candidate is **deferred**
  behind Phase 23-25 because it requires either an upstream
  interface change (Option A) or accepts a strictly worse
  fallback (Option B / C), AND because concrete user demand has
  not been established.

---

## Phase 27+ (future)

(reserved for later candidates)
