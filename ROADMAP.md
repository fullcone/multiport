# Tailscale srcsel ROADMAP

Forward-looking work items not yet scheduled into a phase. Items here
are *candidate* designs, not commitments. They become real PRs only
when picked up by a phase doc.

---

## Phase 21 (candidate) — Dynamic multi-endpoint advertise

**Problem.** A `tailscaled` server today advertises **one** NAT-mapped
public endpoint, the one its primary socket's STUN exchange resolved.
Operators who place a server behind several public IP:port front
doors (multiple DNAT rules, e.g. `P1:port → S:port`,
`P2:port → S:port`, `P3:port → S:port`) cannot get clients to use
those alternative front doors: clients only know about the single
STUN-discovered endpoint plus DERP relays, so the extra DNAT rules
are dead weight.

**Why this is *not* covered by Phase 1-20 srcsel.** Source-path
selection (Phases 1-20 + W-series validation) is about the **sender**
choosing which of its multiple local UDP sockets to send from. It
does not change the **receiver**'s endpoint advertisement. The
receiver still publishes one (`endpoint`, `socket`) tuple per
socket, all derived from STUN.

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
2. **Watcher in `magicsock`**: a goroutine that watches the file via
   `fsnotify` (Linux inotify / macOS FSEvents / Windows
   ReadDirectoryChangesW). On change:
   - Re-read the file.
   - Diff against the current set of "extra advertised endpoints".
   - Push a synthetic `magicsock.endpointGather` cycle that includes
     the new set alongside the STUN-derived primary.
3. **Control plane propagation**: the gathered endpoint set is
   already pushed to the control plane (headscale / Tailscale SaaS)
   via the existing `Hostinfo.Endpoints` flow. Headscale fans out
   the change as a peer-list update; existing clients receive it
   over their long-poll netmap subscription within ~1 s.
4. **Client side**: no change required. magicsock already iterates
   over peer endpoints when establishing direct paths; a fresh set
   means the next path probe will try the new IP:port.
5. **Polling fallback**: for filesystems where fsnotify is unreliable
   (e.g. some network mounts), allow a polling interval via env
   `TS_EXPERIMENTAL_EXTRA_ENDPOINTS_POLL=10s`.
6. **Removal semantics**: when an endpoint disappears from the file,
   the watcher pushes a netmap update with that endpoint absent.
   Already-connected clients whose `bestAddr` was that endpoint will
   detect failure on next packet (or via `noteRecvActivity` quiet
   timeout) and re-probe.

### Open questions

- **Authentication for extra endpoints.** A peer can already lie about
  its own IP/port today (any value goes through STUN); the file
  watcher does not change the trust model — we still rely on the
  WireGuard handshake to confirm the peer at the address. So no new
  auth surface, but it is worth documenting explicitly in the doc.
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
