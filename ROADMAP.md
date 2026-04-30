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

## Phase 22 (candidate) — Total-path latency-aware peer-relay selection

**Problem.** When two peers A and C cannot establish a direct UDP
path (or the operator wants to deliberately avoid the direct path),
Tailscale today relays the traffic through either DERP (centralized
servers in fixed regions) or a peer-relay (any node that has
`tailcfg.Hostinfo.PeerRelay = true`). The peer-relay candidate set
can be large — anyone on the tailnet who opted in. **Selection
between candidate peer-relays is single-segment A↔relay only**: the
existing `relayManager` ranks relay candidates by the RTT each one
returns on its `CallMeMaybeVia` reply (the round-trip A → relay →
A), without any signal on what happens *after* the relay sends the
packet onward to C.

In practice this can pick badly. Concrete scenario the user
described:

```
   A ↔ N (peer relay, RTT 10 ms)         N ↔ C (geographically far,   80 ms)
   A ↔ M (peer relay, RTT 20 ms)         M ↔ C (close to C in Asia,  10 ms)

   Optimal end-to-end: A → M → C  total 30 ms
   Tailscale today picks: A → N → C  total 90 ms (because A↔N has the lowest first-hop RTT)
```

**Why neither Tailscale nor ZeroTier has it today.**

- *Tailscale*: `relayManager.startUDPRelayPathDiscoveryFor` (in
  `wgengine/magicsock/relayserver*.go`) runs disco probes with each
  relay candidate and ranks by the round-trip `A → relay → A` RTT.
  It does not query the relay for "what is *your* RTT to peer C".
  No protocol exists for that question.
- *ZeroTier* (verified against `zerotier/ZeroTierOne` HEAD,
  `node/Topology.cpp::getUpstreamPeer` + `node/Peer.hpp::relayQuality`):
  the relay-selection score is
  `latency_to_relay * (staleness_factor + 1)`. Single-segment only,
  exactly the same gap. ZeroTier's `Bond.cpp` is a separate
  multi-physical-path bonding for the A↔B link, not multi-relay
  selection.

**Why this is *not* covered by Phase 21.** Phase 21 (multi-endpoint
advertise) lets a node publish *its own* extra DNAT'd entry points.
It does not change how clients pick *which relay* to forward through,
nor does it introduce any A→peer→C end-to-end probing. Phase 21 and
Phase 22 are orthogonal.

### Sketch

1. **New disco frame `PeerRTTQuery` / `PeerRTTReport`**:
   - `PeerRTTQuery{target_pubkey: NodeKey}` sent A → B, asking
     "what is your most-recent RTT to C?".
   - `PeerRTTReport{target_pubkey, mean_rtt_ms, sample_age_ms,
     sample_count}` returned B → A. B reports its 60-s mean RTT to
     C (reusing Phase 19's existing per-(dst, source) sample window
     it already maintains for srcsel scoring).
   - Frames carry the same encryption / replay-protection envelope
     as existing disco messages. No new key material.
2. **Aggregation layer in `relayManager`** (existing struct in
   magicsock):
   - For each candidate relay B already discovered via the existing
     `CallMeMaybeVia` flow, periodically issue `PeerRTTQuery(C)` for
     each direct-undeliverable peer C the local node has.
   - Build a sparse table `peerRTT[B][C]` = (mean_rtt_ms,
     sample_age_ms). Stale entries (>60 s) get discarded on lookup.
3. **Total-path scoring**:
   - For each candidate relay B reachable from A:
     `score(B) = rtt_A_B + peerRTT[B][C].mean_rtt_ms`
   - Pick `argmin score`. Apply Phase 20-style relative gate: only
     switch to a new relay choice if its score beats the current
     relay's score by ≥10 %.
   - If no `PeerRTTReport` is available for a (B, C) pair within the
     TTL, fall back to the current "single-segment A↔B RTT" ranking
     for that pair only.
4. **Force-relay env knob**:
   - `TS_DEBUG_NEVER_DIRECT_UDP` *already exists* (in
     `wgengine/magicsock/debugknobs.go:65-68`) and disables direct
     UDP entirely, forcing DERP or peer-relay. Phase 22 reuses it
     and does not introduce a new global force-relay knob.
   - For finer control (force-relay per-destination instead of
     globally), expose `TS_EXPERIMENTAL_RELAY_PATH_OPTIMIZE_HINTS`
     accepting a comma-separated list of NodeKeys for which the
     optimizer should always be preferred even when direct is
     working.
5. **New metrics**:
   - `magicsock_relay_total_path_query_sent`,
     `..._report_received`,
     `..._relay_switched_due_to_total_path`,
     `..._relay_kept_due_to_gate`.
6. **Opt-in default**: gated on
   `TS_EXPERIMENTAL_RELAY_PATH_OPTIMIZE=true`. Off by default;
   without it, the existing single-segment scoring runs unchanged.

### Open questions

- **Probe overhead**. A relay B carries traffic for many peers
  simultaneously. Querying B for "RTT to every C in your peer
  list" creates O(peers) probe load on each relay. Mitigations:
  rate-limit per (A, B) at one query per 30 s; piggy-back the
  query in existing keep-alive frames; cap the candidate-relay set
  size.
- **Relay honesty**. A malicious relay could lie about its RTT to
  C to attract traffic (or repel it). Same threat model as
  endpoint advertising: WireGuard handshake at the destination
  authenticates payload, so a lying relay can route *garbage* but
  not impersonate. Operators in zero-trust deployments who care
  about path-honesty can disable Phase 22 and stick with
  single-segment scoring.
- **Sample-source consistency**. Phase 19's per-(dst, source)
  sample window is keyed by *the source socket B used*, not by the
  relay function. Reusing those samples in `PeerRTTReport` is
  fine for the "B → C primary path" reading but doesn't capture
  "B → C via aux source", which may be different. v1 of Phase 22
  reports primary-source mean only; aux-source per-(B, C, source)
  is a v2 enhancement.
- **TTL window vs. relay selection cadence**. The existing 60-s
  TTL for srcsel samples is the natural reuse here. Relay
  selection change cadence should be slower (suggest 2× the
  sample TTL = 120 s minimum dwell time) to avoid flap when
  multiple relays' total-path RTT are within the gate threshold.
- **Interaction with `TS_DEBUG_NEVER_DIRECT_UDP`**. When the global
  force-relay knob is on, every peer must go via *some* relay.
  Phase 22's optimizer applies to all peers in that case, not just
  hint-listed ones. Document explicitly.
- **Metric blowup with N peers**. The `peerRTT` table is O(relays
  × peers). For a tailnet with 30 relays and 1000 peers, that is
  30 000 entries with TTL=60 s — manageable. For larger tailnets
  the cap should be at most the top-K relays (by single-segment
  score) actively probed.

### Out of scope for the candidate

- Beyond-2-hop relay chains (A → B1 → B2 → C). Phase 22 only
  measures the immediate next-hop relay. Multi-hop chaining would
  need a recursive query/report flow and a path-quality protocol.
- Bandwidth-aware selection. Phase 22 measures latency only, not
  throughput or packet-loss. A relay with low latency but a
  congested last-mile to C still gets picked. v2 could add
  loss-rate from existing send-failure counters.
- Geographic / political routing constraints. Phase 22 picks the
  relay with the lowest *measured* total RTT, regardless of which
  jurisdiction the relay sits in. Operators with regulatory
  constraints would need a per-relay allow/deny policy
  layer above Phase 22's selector.
- Cross-tailnet relays. Phase 22 only considers peer-relays inside
  the same headscale-coordinated tailnet (same `Hostinfo` set).
  Cross-tailnet federation is a separate piece.

### Estimated effort

- New disco frames + relayManager aggregation + selection rewrite:
  ~400 LoC Go.
- env knob plumbing + tests: ~150 LoC test.
- Phase doc + multi-relay bilateral validation harness (would need
  ≥3 relays + ≥2 endpoints to demonstrate the picking): ~400 LoC
  Python + 1 phase doc.
- Total: roughly the size of Phase 19 (TTL/min-samples scorer) +
  Phase 21 combined.

### Why this matters operationally

The single-segment-only scoring works fine when the relay set is
geographically clustered or when the destination C is reachable
from any relay at uniform latency. It fails when **the relay set is
geographically diverse** (the typical "global relay mesh" topology
operators build to handle international tailnets). In that case a
nearby-by-A relay can be far-from-C and a far-by-A relay can be
near-C; without total-path probing the optimizer has no way to see
the second cost. Phase 22 closes that visibility.

---

## Phase 23+ (future)

(reserved for later candidates)
