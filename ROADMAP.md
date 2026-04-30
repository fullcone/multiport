# Tailscale srcsel ROADMAP

Forward-looking work items not yet scheduled into a phase. Items here
are *candidate* designs, not commitments. They become real PRs only
when picked up by a phase doc.

---

## Phase 21 (candidate) — Dynamic multi-endpoint advertise

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

## Phase 22 (candidate) — Direct-vs-relay latency-aware switching

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
2. **Cross-category comparison gate.** `bestAddr` already picks
   the lowest-latency candidate in its current pool. Phase 22 just
   ensures both direct and relay candidates are *in* the pool
   simultaneously. To prevent flap when their latencies are close,
   apply a Phase 20-style 10 % relative gate for the cross-category
   transition specifically:
     - currently direct, relay candidate's mean latency
       < `direct.latency × (1 - 10 %)` ⇒ switch to relay.
     - currently relay, direct candidate's mean latency
       < `relay.latency × (1 - 10 %)` ⇒ switch to direct.
     - otherwise stay put.
   The 60-s sample TTL (`sourcePathSampleTTL`) is the natural
   smoothing window — reuse it.
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

## Phase 23+ (future)

(reserved for later candidates)
