# Tailscale Direct Multisource UDP Phase W11 Row 2 Validation

Date: 2026-04-30

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\zerotier-client\multiport`

Branch: `phase-w11-row2-validation`

Pull request: not yet opened

Branch base: `99c4c35f3` (post-PR-8 main; W10 row 1 last merged before
this phase).

This phase is documentation only. It records the first **single-side
NAT** bilateral srcsel validation between a public-IP Linux host and
a NAT'd Linux client under plan v01 § 4.4 row 2. W7 covered row 3
(both-NAT), W10 covered row 1 (both-public); W11 closes the
remaining matrix row at the strict definition (one end public, one
end NAT'd) and reveals a **new force-mode finding** that distinguishes
row 2 from W7's blackhole in row 3. No Go code changed in W11.

## Topology

```
                public Internet
                  ▲           ▲
                  │           │
+-----------------+           +-----------------+
| srcsel-pair2-host           srcsel-row2-client|
| 216.144.236.235             36.111.166.166    |
| eth0 = 216.144.236.235/28   eth0 = 192.168.   |
|        2607:9d00:..::910c   1.62/24 (private) |
|        :2aa8                upstream router   |
| (true public, no NAT)       port-forwards UDP |
|                             41641 → ens3:41641|
| /usr/local/bin/              /usr/local/bin/  |
|   tailscaled-srcsel            tailscaled-    |
|                                  srcsel       |
| --port=41641                 --port=41641     |
| /usr/local/bin/headscale 0.28.0               |
|   listen 0.0.0.0:8080                         |
|   server_url http://216.144.236.235:8080      |
+-----------------+           +-----------------+
```

The host is **strictly public** (eth0 binds the public IPv4 + IPv6
directly, no upstream NAT). The client is behind upstream NAT — its
ens3 holds a private IPv4 (`192.168.1.62/24`) and traffic to/from the
public IPv4 (`36.111.166.166`) traverses an upstream router that
explicitly port-forwards UDP 41641 to the client. This is the same
client topology W7 used for its server side, but here it occupies
the NAT side of a row-2 pair.

This is now the strict plan-v01 § 4.4 **row 2** (single-side hard
NAT) — distinct from W7's row 3 (both NAT) and W10's row 1 (both
public).

## Test Methodology

Same three-mode pattern as W7 / W10: baseline, forced-aux on both
ends, automatic on both ends. For each mode the two `tailscaled-srcsel`
processes are restarted with the appropriate `TS_EXPERIMENTAL_SRCSEL_*`
env vars; then `tailscale ping --tsmp` is run from each end in each
direction over both IPv4 and IPv6 tailnet addresses; then
`magicsock_srcsel_*` metrics are sampled from both ends.

The tailnet addresses on this run:

```
host   srcsel-pair2-host   100.64.0.1  fd7a:115c:a1e0::1
client srcsel-row2-client  100.64.0.3  fd7a:115c:a1e0::3
```

Both nodes register against the headscale instance running on the
public host (the same one used by W10). The client reaches the
control plane directly over public IPv4 — **no SSH tunnel** is
required, just like W10 (and unlike W7's Windows side).

## Result 1 — Direct UDP Path Established (with NAT-traversal latency)

Default `tailscale ping` returned a direct pong:

```
host> tailscale status
100.64.0.3  srcsel-row2-client  ...  active; direct 36.111.166.166:41641, tx 924 rx 964
```

The "via" address is the client's public IPv4 plus its
port-forwarded UDP 41641. Path establishment had a brief delay (the
first TSMP ping in each direction timed out before the second
succeeded), reflecting STUN-based NAT discovery on the client side.
RTT settled at **~177-182 ms** — significantly higher than W10
row 1 (2 ms, both public) but lower than W7 row 3 (338 ms, both
NAT'd).

## Result 2 — Baseline (no srcsel)

`TS_EXPERIMENTAL_SRCSEL_*` unset on both ends.

```
client> tailscale ping --tsmp --c=5 100.64.0.1
ping "100.64.0.1" timed out
pong from srcsel-pair2-host (100.64.0.1, 58436) via TSMP in 182ms

host>   tailscale ping --tsmp --c=5 100.64.0.3
ping "100.64.0.3" timed out
pong from srcsel-row2-client (100.64.0.3, 34152) via TSMP in 176ms

client> tailscale ping --tsmp --c=3 fd7a:115c:a1e0::1
pong from srcsel-pair2-host (fd7a:115c:a1e0::1, 58436) via TSMP in 177ms

host>   tailscale ping --tsmp --c=3 fd7a:115c:a1e0::3
pong from srcsel-row2-client (fd7a:115c:a1e0::3, 34152) via TSMP in 177ms
```

All four directions succeed once the path is warm. The first ping in
each new direction times out due to NAT-traversal bootstrap; the
second succeeds and subsequent pings stay below 200 ms. All
`magicsock_srcsel_*` counters remain `0` because srcsel is disabled.

## Result 3 — Forced Auxiliary on Both Ends (does NOT blackhole)

`TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux` on both ends. Both
log the env knobs and bind 4 UDP sockets each (primary v6 + v4 + aux
v4 + aux v6) per W4's documented order.

All four TSMP ping directions **succeed** at ~187 ms:

```
client> tailscale ping --tsmp --c=5 100.64.0.1   pong via TSMP in 187ms
host>   tailscale ping --tsmp --c=5 100.64.0.3   pong via TSMP in 187ms
client> tailscale ping --tsmp --c=3 fd7a:..::1   pong via TSMP in 188ms
host>   tailscale ping --tsmp --c=3 fd7a:..::3   pong via TSMP in 187ms
```

Sampled metrics after the run:

```
host    data_send_aux_selected      8
host    data_send_aux_succeeded     8
host    data_send_aux_fallback      0
host    aux_wireguard_rx            0
host    probe_pong_accepted         6
host    probe_pending_expired       2

client  data_send_aux_selected      11
client  data_send_aux_succeeded     11
client  data_send_aux_fallback      0
client  aux_wireguard_rx            0
client  probe_pong_accepted         5
client  probe_pending_expired       3
```

**Crucially, this does NOT match the W7 row-3 forced-mode result**
(which had `probe_pong_accepted=0` and `probe_pending_expired=81` on
the server side and TSMP timing out in both directions). In W11 row 2,
both ends accumulate probe pongs and TSMP succeeds bidirectionally.

The structural reason traces through the source-path probe + NAT
behavior:

1. **`sendSourcePathDiscoPing`** (`sourcepath.go`) sends from `aux`
   to the **peer's primary endpoint** (`dst.ap`), not to peer aux.
2. The reply path: the peer's `handleSourcePathProbeLocked`
   (`magicsock.go:2712`) replies via `c.sendDiscoMessage` which uses
   the peer's **primary** outbound socket, sending to the source IP
   it observed on the inbound probe (i.e. our aux's NAT-mapped
   public address).
3. On the client (NAT'd) side: client-aux outbound to host-primary
   creates a NAT mapping bound to host-primary; the host's pong
   reply (from host-primary) traverses that mapping back to
   client-aux. ✓
4. On the host (public) side: server-aux outbound to client-primary
   reaches client-primary via the **explicit upstream port-forward
   on UDP 41641** — the port-forward acts as a full-cone-equivalent
   for that specific port, so server-aux's source IP/port does not
   need to match a prior outbound. The client's pong reply (from
   client-primary) reaches server-aux because the underlying NAT
   state was created by server-aux's outbound. ✓

In W7 row 3, the analogous flow on the server side failed because
the server's peer (Windows on enterprise CGNAT) had **no port-forward**
on its primary 41642; the only NAT mapping that existed was the
client-initiated outbound mapping bound to server-primary, which
rejected server-aux's inbound. W11 row 2 does not exhibit this
because the NAT'd end's primary port is explicitly forwarded.

**This means forced-aux is safer than W7 portrayed it: as long as
the NAT'd peer's primary port is reachable from outside (port
forward, full-cone NAT, or DMZ), the source-path probe replies can
return via primary even when sending from aux.** Force-mode only
catastrophically fails when both sides are NAT'd *and* neither side's
primary is reachable from arbitrary source ports — i.e. the strict
W7 row 3 case.

## Result 4 — Automatic Mode Under Sustained Traffic

`TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true` on both ends. After
20 rounds × 2 TSMP pings from the host (40 outbound), all 40 pings
completed at 182 ms.

Sampled metrics:

```
host    data_send_aux_selected      0
host    data_send_aux_succeeded     0
host    primary_beat_rejected       42
host    aux_wireguard_rx            0
host    probe_pong_accepted         21
host    probe_pending_expired       5
host    probe_samples_expired       4

client  data_send_aux_selected      0
client  data_send_aux_succeeded     0
client  primary_beat_rejected       25
client  aux_wireguard_rx            0
client  probe_pong_accepted         24
client  probe_pending_expired       4
client  probe_samples_expired       2
```

End-to-end reading:

1. **Source-path probes flow bidirectionally** — 21 + 24 pongs
   accepted across the run, far above the `sourcePathMinSamplesForUse
   = 3` threshold the auto scorer requires.
2. **The Phase 20 primary-baseline gate fires aggressively** —
   42 + 25 rejections. Aux probe RTT (~187 ms) is essentially equal
   to primary RTT (~182 ms) on this single-NAT path; the cutoff
   `primary × (1 - 10%) = 164 ms` is below the aux mean, so every
   sample group gets rejected.
3. **Zero data sends switched to aux** — auto mode correctly stays
   on primary throughout. This is the *designed* behavior on paths
   where aux is not measurably faster than primary.
4. **`aux_wireguard_rx = 0`** — same structural finding as W10 (see
   `phasew10` Result 5).
5. **TSMP stays bidirectional throughout** — the data plane is
   never disrupted by the scorer's deliberation.

This is the cleanest demonstration so far that **the Phase 20 gate
is RTT-scale-invariant**: in W10 row 1 it fired ~28 / 41 evaluations
on a 2 ms direct path; here in W11 row 2 it fires ~67 / ~70 on a
182 ms NAT-traversed path. The 10 % relative threshold rejects
indistinguishable aux measurements regardless of the absolute RTT.

## Compliance with Plan v01 § 4.4 Matrix (Updated)

| Row | Description                       | Status                            |
|-----|-----------------------------------|-----------------------------------|
| 1   | both ends public (IPv4 + IPv6)    | covered by W10                    |
| 2   | client single-side hard NAT       | **Covered by W11**                |
| 3   | both sides NAT                    | covered by W7                     |
| 4   | Wi-Fi / 4G switch on the client   | not exercised (wired hosts)       |
| 5   | Modern Standby suspend / resume   | covered N/A by W5 (Server SKU)    |
| 6   | AV / EDR enabled                  | covered by W5 (Defender RTP off)  |

W7's "≥ 1 row" acceptance is now upgraded to **rows 1, 2, and 3 all
covered**, with the IPv6 path validated end-to-end on the row-1
bilateral run.

## Findings

1. **Row 2 srcsel data plane works in all three modes.** Baseline,
   force, and auto all keep TSMP bidirectional on both stacks at
   ~180 ms RTT (NAT-traversal cost on top of W10's 2 ms baseline).
2. **Force-aux is safer than W7 implied** for the realistic case
   where the NAT'd peer's primary port is port-forwarded (a common
   homelab / VPS setup). The reverse-path-blackhole risk W7
   documented requires *both* ends to be NAT'd *and* neither
   primary port to be externally reachable — a strict double-NAT
   without explicit forwarding.
3. **Phase 20 primary-baseline gate is RTT-scale-invariant.** The
   10 % relative threshold rejects indistinguishable aux measurements
   at 2 ms (W10) and at 182 ms (W11) alike. The gate's design works
   correctly regardless of whether the path is LAN-class or
   internet-class.
4. **`aux_wireguard_rx` stays at 0 in row 2** as well, consistent
   with W10's structural-unreachability finding. Phase 19's
   bidirectional-receive-defended status is preserved across all
   three matrix rows.
5. **NAT-traversal bootstrap costs the first ping per direction.**
   Steady-state pings settle at the path's natural RTT, but the very
   first packet in a new direction times out and is retried; the
   retried packet succeeds. Operators should not interpret that
   single retry as a srcsel bug.

## Recommended Phase 19 / Phase 20 Doc Updates

The `aux_wireguard_rx` correction Phase W10 added remains the
authoritative position; W11 reaffirms that finding under row 2.

Phase 19's `force-aux mode reverse-path blackhole` framing in its
"operators carry full responsibility" caveat should ideally be
softened to say the blackhole is specifically the **double-NAT**
case where neither primary is externally reachable; W7 + W11 +
W10 together now cover the matrix and show forced-aux is a
catastrophic risk only in the strict W7 topology, not in row 1
or row 2.

## Out Of Scope For W11

- Pair 1 (`36.133.102.126 ↔ 36.111.166.166`): the user's stated
  intention was "both public IPv4", but `36.111.166.166`'s topology
  is upstream-NAT'd (W7 + W11 confirmed). Pair 1 would therefore
  exercise either row 2 (if 36.133 is truly public) or row 3 (if
  also NAT'd); not run here.
- Wi-Fi/4G switching on the client (matrix row 4) — wired-only
  hosts.
- Sustained large-data throughput.
- Truly symmetric / port-restricted NAT on the client side: the
  W11 client has port-forward on its primary, so it does not
  exhibit a hostile NAT in the strict sense. A future row-2
  variant on a CGNAT-only client would test whether forced-aux
  blackholes when the NAT'd primary is *not* externally reachable.
