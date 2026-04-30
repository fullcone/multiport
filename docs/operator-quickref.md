# Operator quick reference — srcsel + Phase 21 + Phase 22 v2

One-page TL;DR for operators who already have tailscaled-srcsel on a node
and just want to know "which env knob solves my problem".

For full deployment from scratch, see
[operator-deploy-runbook.md](operator-deploy-runbook.md).

---

## srcsel core (Phase 1-20)

Always-on data-plane multi-source UDP. Three modes:

| Mode | Env | When to use |
|---|---|---|
| `baseline` | `TS_EXPERIMENTAL_SRCSEL_*` unset | Disable srcsel entirely. Behavior identical to stock Tailscale. |
| `force` | `TS_EXPERIMENTAL_SRCSEL_ENABLE=true`<br>`TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1`<br>`TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux` | **Diagnostic only** — every data send goes through aux socket. Use to measure aux-side reachability and reproduce W7 row-3 blackhole behavior. |
| `auto` | `TS_EXPERIMENTAL_SRCSEL_ENABLE=true`<br>`TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1`<br>`TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true` | **Production**. Phase 19 TTL/min-samples scorer + Phase 20 primary-baseline gate decide aux vs primary per-(dst, source); falls back to primary on insufficient signal or worse aux RTT. |

---

## Phase 21 — dynamic multi-endpoint advertise (server / receiver side)

Advertise **multiple public IP:port front doors** dynamically. Operator
maintains a JSON file; magicsock watches it and propagates changes to all
peers within ~1 s via the existing control-plane `MapRequest.Endpoints`
flow.

```bash
# Enable
export TS_EXPERIMENTAL_EXTRA_ENDPOINTS_FILE=/etc/tailscaled/extra-endpoints.json

# Maintain
cat > /etc/tailscaled/extra-endpoints.json <<EOF
{"endpoints": ["P1:41641", "P2:41641", "P3:41641"]}
EOF
chmod 0644 /etc/tailscaled/extra-endpoints.json
chown root:root /etc/tailscaled/extra-endpoints.json

# Edit the file at any time (echo / sed -i / orchestrator-driven write).
# tailscaled re-reads via fsnotify, peers see the new set within ~1 s.
```

**Tunables**:

| Env | Default | Purpose |
|---|---|---|
| `TS_EXPERIMENTAL_EXTRA_ENDPOINTS_FILE` | `""` (off) | JSON file path. **Master switch**. |
| `TS_EXPERIMENTAL_EXTRA_ENDPOINTS_MAX` | `0` (unlimited) | Optional defense-in-depth policy cap on entries per parse. Unset = honor every entry below the file-size memory ceiling. Set to a positive integer only if you want a hard upper bound on what an upstream orchestrator can publish under this node's identity. |
| `TS_EXPERIMENTAL_EXTRA_ENDPOINTS_POLL_S` | `0` (off) | Polling fallback for filesystems where fsnotify is unreliable (NFS, some FUSE). Set e.g. `30`. |

**Safety**:

- File mode must NOT be group- or world-writable on Linux/macOS — watcher refuses 0660 / 0666 etc. Use 0644 (or stricter 0640 / 0600).
- File-size memory ceiling: 64 MB. Sized for a 100 000-entry baseline with ~10× headroom; files larger than 64 MB are refused at read. Pure memory-safety guard, not a policy constraint.
- WireGuard handshake at the destination still authenticates the data plane, so a peer publishing fictional endpoints can route garbage but can't impersonate.

**Metrics**:

```
magicsock_extra_endpoints_reads     # successful parse counter
magicsock_extra_endpoints_reloads   # parses where the set actually changed (each → ReSTUN)
```

---

## Phase 22 v2 — direct-vs-relay latency-aware switching (client / sender side)

Lift Tailscale's default "direct-UDP-always-wins" preference. When the
opt-in is set, magicsock periodically measures peer-relay candidates'
end-to-end RTT and switches to a relay if it beats direct by ≥10 % (with
hysteresis to prevent flap).

```bash
# Enable
export TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE=true

# Optional: react faster (default is 5 min)
export TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE_INTERVAL_S=60

# Optional: be more aggressive (default 10%)
export TS_EXPERIMENTAL_DIRECT_VS_RELAY_THRESHOLD_PCT=5
```

**Tunables**:

| Env | Default | Purpose |
|---|---|---|
| `TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE` | `false` | **Master switch**. When unset, behavior is bit-identical to before — direct UDP wins unconditionally. |
| `TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE_INTERVAL_S` | `300` (5 min) | How often to probe relay candidates while a direct path is held. Floored at the existing 30 s `discoverUDPRelayPathsInterval`. Smaller = faster reaction, more probe overhead. |
| `TS_EXPERIMENTAL_DIRECT_VS_RELAY_HOLD_S` | `300` (5 min) | Per-peer hysteresis after a direct↔relay swap — no further category swaps for this peer until this elapses. Larger = more stable, slower to undo a wrong choice. |
| `TS_EXPERIMENTAL_DIRECT_VS_RELAY_THRESHOLD_PCT` | `10` | Phase 20-style relative gate. Alternative must be at least this many percent faster to win the swap. `100` = never swap; negative = disable gate (instant swap on any improvement; not recommended). |

**Metrics**:

```
magicsock_direct_vs_relay_compared           # comparison-mode discovery cycles
magicsock_direct_vs_relay_gate_relay_preferred   # gate chose relay over direct
magicsock_direct_vs_relay_gate_direct_preferred  # gate kept direct
magicsock_direct_vs_relay_hold_rejected      # cross-category swap blocked by hold window
```

---

## Common deployment scenarios

### Scenario 1 — multi-public-IP rotating server (Phase 21 only)

```bash
# Server side (also enable srcsel auto for the data plane)
export TS_EXPERIMENTAL_SRCSEL_ENABLE=true
export TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1
export TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true

export TS_EXPERIMENTAL_EXTRA_ENDPOINTS_FILE=/etc/tailscaled/extra-endpoints.json
# Maintain extra-endpoints.json with the live front-door pool;
# every edit is hot-applied, peers learn about it within ~1 s.
```

Use when: external load-balancer / "rotating IPs" controller manages a
pool of public IP:port DNATs to a single tailscaled.

### Scenario 2 — pick best total-path through any peer-relay (Phase 22 v2)

```bash
# Client side
export TS_EXPERIMENTAL_SRCSEL_ENABLE=true
export TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1
export TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true

export TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE=true
# Optional, more responsive:
# export TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE_INTERVAL_S=60
# export TS_EXPERIMENTAL_DIRECT_VS_RELAY_THRESHOLD_PCT=5
```

Use when: client-server direct path is BGP-suboptimal (cross-continental
detours), and there's a peer-relay closer to the destination that would
yield lower total RTT.

### Scenario 3 — hard-force all traffic through relay (no direct ever)

```bash
# Client side — uses the existing magicsock debug knob, NOT a Phase 22 knob.
export TS_DEBUG_NEVER_DIRECT_UDP=true
```

Use when: you want to **always** avoid direct UDP — for testing relay
paths, censorship-routing scenarios, or when direct UDP is broken in
your environment. magicsock will then evaluate peer-relay candidates by
their end-to-end RTT (this part is upstream Tailscale behavior, not
Phase 22).

This **subsumes** Phase 22 v2: there is no direct path to compare
against, so the gate is moot.

### Scenario 4 — combined (server side does Phase 21 + auto srcsel)

```bash
# Server side
export TS_EXPERIMENTAL_SRCSEL_ENABLE=true
export TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1
export TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true
export TS_EXPERIMENTAL_EXTRA_ENDPOINTS_FILE=/etc/tailscaled/extra-endpoints.json

# Client side: srcsel auto + Phase 22 active comparison
export TS_EXPERIMENTAL_SRCSEL_ENABLE=true
export TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1
export TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true
export TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE=true
```

Recommended for production deployments where:
- The server has multiple public IPs (Phase 21 gives clients all entry options),
- The client may be on a long-haul path where a peer-relay shortcut exists (Phase 22 picks it when measurably better),
- srcsel auto handles the per-(dst, source) data-plane source-socket selection inside each candidate path.

---

## Quick check: is the feature actually on?

```bash
# Server (Phase 21):
sudo journalctl -u tailscaled-srcsel | grep extra-endpoints | head -5
# Expected:
#   magicsock: extra-endpoints: loaded N endpoint(s) from "..."

sudo /usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock \
    debug metrics | grep extra_endpoints
#   magicsock_extra_endpoints_reads <N>     ← > 0 means watcher ran

# Client (Phase 22 v2):
sudo /usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock \
    debug metrics | grep direct_vs_relay
#   magicsock_direct_vs_relay_compared <N>  ← > 0 after first 5-min cycle
```

If the metrics are stuck at 0 after sufficient runtime, the env knobs
likely didn't apply (env-var leak in systemd unit, missed quoting, etc.).
Confirm via `journalctl -u tailscaled-srcsel | grep envknob`.
