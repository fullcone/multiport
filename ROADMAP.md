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

## Phase 22+ (future)

(reserved for later candidates)
