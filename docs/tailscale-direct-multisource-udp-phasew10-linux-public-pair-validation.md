# Tailscale Direct Multisource UDP Phase W10 Linux Public-Pair Validation

Date: 2026-04-30

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\zerotier-client\multiport`

Branch: `phase-w10-linux-public-pair-validation`

Pull request: not yet opened

Branch base: `3fef8bd94` (post-PR-7 main; W7 + W7 scripts archive last
merged before this phase).

This phase is documentation only. It records the first **both-public**
bilateral srcsel validation between two public-IP Linux hosts under
plan v01 § 4.4 row 1 (IPv4) and row 1-dual-stack (IPv4 + IPv6). W7
covered row 3 (double-NAT); W10 covers row 1, fills the W3 Windows
IPv6 deferral with a Linux-IPv6 data point at the same magicsock
layer, and **corrects the Phase 19 doc's overoptimistic claim that
`magicsock_srcsel_aux_wireguard_rx` would become non-zero in row 1**.
No Go code changed in W10.

## Topology

Two public-dual-stack VPS hosts, both Ubuntu 24.04, both with
public IPv4 and public IPv6 routed to a single `eth0`-class NIC:

```
                   +----- public Internet -----+
                   |                           |
+------------------+                           +------------------+
| srcsel-pair2-host                            srcsel-pair2-client|
| 216.144.236.235     <===  direct UDP  ===>   64.64.254.58       |
| 2607:9d00:..::910c:2aa8                      2607:8700:..::2    |
| eth0 (public IPs)                            eth0 (v4) +        |
|                                              ipv6net (v6)       |
| /usr/local/bin/tailscaled-srcsel             tailscaled-srcsel  |
| --port=41641                                 --port=41642       |
| /usr/local/bin/headscale 0.28.0                                 |
|   listen 0.0.0.0:8080                                           |
|   server_url http://216.144.236.235:8080                        |
+------------------+                           +------------------+
```

Both ends register against the same headscale instance running on
the host server. Unlike W7, **no SSH tunnel is required** — the
client reaches the headscale control plane directly over public IPv4.
iptables INPUT default is ACCEPT on both hosts plus an explicit
`tcp 8080` allow on the host for the control plane.

## W10 Scope

| plan v01 § 4.4 row | W10 status                                                                  |
|--------------------|-----------------------------------------------------------------------------|
| 1 — both ends public, no NAT (IPv4)         | **Covered** (this phase)                       |
| 1 — both ends public dual-stack (IPv6 path) | **Covered** (this phase, fills W3 deferral end-to-end at the magicsock layer) |
| 3 — both sides NAT                          | covered by W7                                  |

Plan v01 § 4.4 originally targeted Windows ↔ Linux pairs. W10 uses
Linux ↔ Linux because the magicsock + srcsel code path is the same
on both platforms (Phase W1 lifted the build tag to `linux ||
windows`); for row 1's data-plane correctness validation, the OS
distinction at the WireGuard / magicsock layer is moot.

## Test Methodology

Same three-mode pattern as W7: baseline, forced-aux on both ends,
automatic on both ends. For each mode, both `tailscaled-srcsel`
processes are restarted with the appropriate
`TS_EXPERIMENTAL_SRCSEL_*` env vars; then `tailscale ping --tsmp` is
run from each end in each direction over both IPv4 and IPv6 tailnet
addresses; then `magicsock_srcsel_*` counter metrics are sampled.

Auth and pre-auth keys are short-lived headscale-issued tokens. The
W10 orchestration helpers live in
[`scripts/srcsel-w10/`](../scripts/srcsel-w10/) — they share the
W7-style `SRCSEL_W7_*` env knobs for the headscale host and add a
parallel `SRCSEL_W10_CLIENT_*` group for the second pair member.
Run order:

```
scripts/srcsel-w10/
  README.md             prerequisites + env-var contract + run order
  .gitignore            excludes __pycache__/ and *.pyc
  _pair.py              shared paramiko helper (host + client connections)
  01-recon.py           OS / NIC / public-reach probe of both servers
  02-upload-binaries.py sftp Linux binaries to /usr/local/bin/ on both
  03-headscale-setup.py install + reconfigure headscale on host server
  04-both-up.py         restart tailscaled on both with mode env
                        (SRCSEL_W10_MODE = baseline | force | auto)
  05-tsmp-test.py       bidirectional TSMP IPv4 + IPv6 + metric capture
  06-sustained-ping.py  20-round sustained ping from host (exercises Phase 20)
```

These helpers are best-effort, one-time test harness; expect to
tweak them for any other environment.

## Result 1 — Direct UDP Path Established Instantly

Disco-level `tailscale ping` (default mode) returned a direct pong
on the first try after registration:

```
host> tailscale ping --c=1 100.64.0.2
pong from srcsel-pair2-client (100.64.0.2) via 64.64.254.58:41642 in 2ms
```

`tailscale status -peers` shows the active path:

```
100.64.0.2  srcsel-pair2-client  ...  active; direct 64.64.254.58:41642, tx 676 rx 772
```

There is no DERP relay involved and no STUN hole-punching delay; the
public-public topology delivers the WireGuard data plane in a single
RTT. RTT is **2 ms** vs **338 ms** in W7 row 3 (NAT-traversed,
inter-province route).

## Result 2 — Baseline (no srcsel)

`TS_EXPERIMENTAL_SRCSEL_*` unset on both ends.

```
client> tailscale ping --tsmp --c=5 --timeout=10s 100.64.0.1
pong from srcsel-pair2-host (100.64.0.1, 58436) via TSMP in 11ms

host>   tailscale ping --tsmp --c=5 --timeout=10s 100.64.0.2
pong from srcsel-pair2-client (100.64.0.2, 46590) via TSMP in 2ms

client> tailscale ping --tsmp --c=3 --timeout=10s fd7a:115c:a1e0::1
pong from srcsel-pair2-host (fd7a:115c:a1e0::1, 58436) via TSMP in 2ms

host>   tailscale ping --tsmp --c=3 --timeout=10s fd7a:115c:a1e0::2
pong from srcsel-pair2-client (fd7a:115c:a1e0::2, 46590) via TSMP in 2ms
```

All four directions over both stacks succeed at 2-11 ms. All
`magicsock_srcsel_*` counters remain `0` because srcsel is disabled.

## Result 3 — Forced Auxiliary on Both Ends

`TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux` on both ends.
Each `tailscaled-srcsel` log shows:

```
envknob: TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS="1"
envknob: TS_EXPERIMENTAL_SRCSEL_ENABLE="true"
envknob: TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE="aux"
```

`ss -ulnp` confirms 4 UDP sockets per process — primary v6, primary
v4, aux v4, aux v6 — matching the W4 bind cluster pattern. On the
host:

```
0.0.0.0:41641     primary v4
0.0.0.0:60977     aux v4
[::]:41641        primary v6
[::]:41729        aux v6
```

All four TSMP ping directions over both stacks **succeed**, in
2-11 ms (same RTT range as baseline):

```
client> tailscale ping --tsmp --c=5 100.64.0.1   pong via TSMP in 11ms
host>   tailscale ping --tsmp --c=5 100.64.0.2   pong via TSMP in 2ms
client> tailscale ping --tsmp --c=3 fd7a:..::1   pong via TSMP in 2ms
host>   tailscale ping --tsmp --c=3 fd7a:..::2   pong via TSMP in 2ms
```

**Crucially, forced-aux mode in row 1 does NOT exhibit the W7
double-NAT reverse-path blackhole.** With both ends public, the
NAT-asymmetry that blocked server-aux outbound under W7 is absent;
both sides' aux outbound reaches the peer's primary endpoint without
the firewall-mapping rejection W7 documented.

Sampled metrics after the run:

```
host    data_send_aux_selected      6
host    data_send_aux_succeeded     6
host    data_send_aux_fallback      0
host    aux_wireguard_rx            0
host    probe_pong_accepted         2

client  data_send_aux_selected      7
client  data_send_aux_succeeded     7
client  data_send_aux_fallback      0
client  aux_wireguard_rx            0
client  probe_pong_accepted         2
```

Both ends actually used aux for data sends, both sides' kernel writes
all succeeded, and both ends accepted source-path probe pongs (2
each). **`aux_wireguard_rx` stayed at 0 even in row 1** — see Result
5 below for the structural reason.

## Result 4 — Automatic Mode Under Sustained Traffic

`TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true` on both ends. After 20
rounds × 2 TSMP pings (40 outbound packets from the host), all 40
pings completed at 2 ms each. Sampled host-side metrics:

```
data_send_aux_selected         13
data_send_aux_succeeded        13
data_send_aux_fallback          0
primary_beat_rejected          28
aux_wireguard_rx                0
probe_pong_accepted            20
probe_pending_expired           2
probe_samples_evicted           0
probe_samples_expired           0
send_failure_invalidated_samples 0
```

Reading this end to end:

1. **20 source-path probes round-tripped successfully** in well under
   the TTL window, confirming aux probing on the public-public path
   works without the asymmetric-NAT issues of W7.
2. **Phase 20 primary-baseline gate fired 28 times** — when the
   probe-derived aux mean was not at least 10 % faster than the
   primary RTT estimate, the scorer correctly refused to switch.
   With primary already at 2 ms, the 10 % threshold (cutoff =
   primary × 0.90 = 1.8 ms) is hard to beat in steady state, so the
   gate firing this often is the expected, designed behavior on
   low-RTT direct paths.
3. **13 sends did go via aux** — when the per-source mean briefly
   beat the primary baseline (probe RTT noise + scheduler jitter on
   the very tight timing budget), the scorer promoted to aux for the
   next batch. These all succeeded locally.
4. **No samples expired or were evicted** — the steady probe rate
   stayed within `sourcePathProbeHistoryLimit = 100000` and inside
   `sourcePathSampleTTL = 60s`.
5. **No real-data send failures** — `data_send_aux_fallback = 0`
   confirms aux outbound was healthy through the run.

This is the cleanest exercise of Phase 20's primary-baseline gate
recorded so far: it correctly distinguishes "aux is statistically
indistinguishable from primary" (rejection) from "aux is faster
right now" (acceptance) on a path where both alternatives are
genuinely close.

## Result 5 — `aux_wireguard_rx` Stays at Zero Even in Row 1

This is the W10-specific finding that **corrects the Phase 19 doc**.

Phase 19 closed out with this caveat:

> Phase 19's removal of the aux WireGuard drop is **defended in
> code** (no regression in receive path) but **not exercised by this
> topology**. A row 1 (both public) or full-cone-NAT-on-the-client
> environment is required to drive a non-zero counter.

W10's row 1 / row 1-dual-stack runs deliberately set out to drive
that counter non-zero. They did not. The counter stays at 0 in
**every** mode (baseline, forced, auto) on **both** ends.

The structural reason, traced through the code:

1. `metricSourcePathAuxWireGuardRx` is incremented in
   `Conn.receiveIPWithSource` (`magicsock.go:1881`) only when the
   incoming packet is `packetLooksLikeWireGuard` and the receive
   socket is auxiliary (non-primary `rxMeta`).
2. For a peer to send a WireGuard data packet to my aux address, the
   peer's `endpoint.send` must pick my aux as the destination
   `udpAddr`. `endpoint.addrForSendLocked` reads
   `udpAddr = de.bestAddr.epAddr` at `endpoint.go:591` (and a parallel
   read at `endpoint.go:680`). Aux selection only changes the
   **source** socket, never the **destination**.
3. `de.bestAddr` is updated by `handlePongConnLocked` based on the
   RTT of disco Pongs, not by data-path observation. Disco Ping/Pong
   exchange always uses primary (the source-path probe is a
   different message type, `disco.SourcePathProbe`, processed by an
   isolated state machine that does **not** influence `bestAddr` —
   see Phase 19 design note in
   `docs/tailscale-direct-multisource-udp-phase19-bidirectional-aux-data.md`).
4. `lazyEndpoint.FromPeer` (`magicsock.go:4536-4555`) does add the
   newly-seen source `epAddr` to `peerMap.byEpAddr`, but it only
   updates the lookup map; it does **not** change `bestAddr` and it
   does **not** add the peer's aux to the candidate-endpoint list
   that future disco Pongs would compete on.
5. Therefore, no peer ever observes my aux address as a candidate
   bestAddr. Peer always sends WireGuard data to my primary. My aux
   socket only ever receives `disco.SourcePathProbe` Pongs (which
   are `packetLooksLikeDisco`, not `packetLooksLikeWireGuard`, so
   they don't increment this metric).

In short: **`aux_wireguard_rx` is essentially defensive
instrumentation under the current Phase 19 design**. The drop
removal in `magicsock.go:1881-1883` keeps the receive path clean
against future regression, but no Phase 19 / Phase 20 pathway
actually directs WireGuard data to peer aux addresses. Driving the
counter non-zero would require either:

- Advertising aux endpoints in disco (so peers learn them as
  candidate bestAddrs);
- Or a future feature that explicitly initiates data sends from
  primary destined to peer-aux for some purpose.

Both are out of scope for the current Phase 19 / Phase 20 design.
The Phase 19 doc's claim that row 1 would drive the counter was
**aspirational**, not reflective of the actual `bestAddr` /
`endpoint.send` flow.

## Compliance with Plan v01 § 4.4 Matrix (Updated)

| Row | Description                       | Status                                     |
|-----|-----------------------------------|--------------------------------------------|
| 1   | both ends public, IPv4            | **Covered by W10**                          |
| 1*  | both ends public dual-stack (IPv6) | **Covered by W10** (Linux↔Linux end-to-end) |
| 2   | client single-side hard NAT       | not yet testable on the available beds     |
| 3   | both sides NAT                    | covered by W7                              |
| 4   | Wi-Fi / 4G switch on the client   | not exercised (all wired hosts)            |
| 5   | Modern Standby suspend / resume   | not testable on Server SKU (covered N/A by W5) |
| 6   | AV / EDR enabled                  | covered by W5 (Defender RTP off baseline)  |

W7 acceptance is "≥1 bilateral matrix row"; with W10 the suite now
covers rows 1 + 3, plus the Phase 19 IPv6 path that W3 had deferred
on the Windows side.

## Findings

1. **Row 1 srcsel data plane works in all three modes.** Baseline,
   force, and auto all keep TSMP bidirectional on both stacks at
   single-digit milliseconds.
2. **Force-mode in row 1 does not blackhole.** The W7
   double-NAT-asymmetry-blackhole risk that motivated Phase 19's
   safety gate is absent when both ends are public. Operators who
   force aux in a public-public deployment get aux outbound and
   primary inbound symmetric, no traffic loss.
3. **Phase 20 primary-baseline gate is correctly active.** With
   primary RTT already at 2 ms, the 10 % aux-must-beat threshold
   acts as a meaningful filter against scorer thrashing on
   low-latency paths. 28 rejections out of 41 evaluations is a
   reasonable steady-state ratio.
4. **`aux_wireguard_rx` is structurally unreachable** in current
   Phase 19 design (see Result 5). The Phase 19 doc's "row 1 would
   drive it non-zero" claim does not match the runtime
   `bestAddr` / `endpoint.send` data flow.
5. **Linux IPv6 srcsel works end-to-end** at the magicsock layer.
   This data point fills the **W3 deferral on the IPv6 side** —
   W3's deferred item was specifically Windows IPv6 loopback, and
   the magicsock-layer behavior is the same on Linux (sourcepath
   build tag is `linux || windows`); Linux↔Linux IPv6 srcsel
   exercising both v4 and v6 aux sockets through `tailscale ping
   --tsmp fd7a:115c:a1e0::*` is sufficient to show the IPv6 path is
   live in production code.

## Out Of Scope For W10

- Pair 1 (`36.133.102.126 ↔ 36.111.166.166`): IPv4-only redundant
  data point. Pair 2 already covers row 1 IPv4 plus IPv6, so Pair 1
  was deliberately not exercised here.
- Wi-Fi/4G switching, AV/EDR, Modern Standby — addressed by W5 / W6
  on Windows or recorded as not applicable.
- Recommending a Phase 21 to actually advertise aux endpoints in
  disco so `aux_wireguard_rx` can become observable. That is a
  scoring/disco enhancement, not a defensive fix; the current Phase
  19 / Phase 20 design intentionally keeps disco on primary.

## Recommended Phase 19 Doc Update

The Phase 19 closeout's `aux_wireguard_rx` caveat should be revised
in a future docs-only PR to describe the metric as defensive
instrumentation rather than as an exercisable counter pending row 1.
This W10 phase doc serves as the authoritative correction for now.
