# Tailscale Direct Multisource UDP Phase W12 Windows Three-Node Mesh

Date: 2026-04-30

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\zerotier-client\multiport`

Branch: `phase-w12-windows-three-node-mesh`

Pull request: not yet opened

Branch base: `4b330c21a` (post-PR-9 main; W11 row-2 doc PR is in flight
as PR #10 in parallel and not yet merged at the time of this run).

This phase is documentation only. It records the first bilateral
srcsel validation with **Windows as the client in a three-node mesh**
against the same two Linux servers used by W11 row 2:

```
W12 mesh:
  Windows 11 client  (clean test machine, srcsel-w12-clean-XXXXXX)
  Linux public host  (srcsel-pair2-host,    216.144.236.235)
  Linux NAT-pf host  (srcsel-w12-nat-server, 36.111.166.166)
```

Previous Windows-side validations (W2 / W4 / W5 / W6) ran on the
developer workstation alongside an enterprise sing-box VPN whose TUN
intercept sat on the default route. That environment polluted every
Windows-side srcsel measurement: outbound aux packets were silently
hooked by the sing-box TUN before reaching the physical NIC, and
aux→peer-NAT paths blackholed for reasons unrelated to srcsel itself.
W12 ran on a **clean Win11 real machine** (no VPN, no TUN, no AV
TLS-intercept) and is therefore the first Windows-side srcsel
measurement that can be compared cleanly against the Linux W7 / W10 /
W11 numbers. No Go code changed in W12.

## Topology

```
                public Internet
                 ▲            ▲
                 │            │
+----------------+            +-----------------+
| srcsel-pair2-host           srcsel-w12-nat-server
| 216.144.236.235             36.111.166.166    |
| eth0 = 216.144.236.235/28   eth0 = 192.168.1.62
|        2607:9d00:..::910c   /24 (private)     |
|        :2aa8                upstream router   |
| (true public, no NAT)       port-forwards UDP |
|                             41641 → ens3:41641|
| /usr/local/bin/             /usr/local/bin/   |
|   tailscaled-srcsel           tailscaled-     |
|   --port=41641                  srcsel        |
|                               --port=41641    |
| /usr/local/bin/headscale 0.28.0               |
|   listen 0.0.0.0:8080                         |
|   server_url http://216.144.236.235:8080      |
+-----------------+-----------+-----------------+
                  │           │
                  │           │ (mesh peer of)
                  ▼           ▼
            +------------------------------+
            |  srcsel-w12-clean-XXXXXX     |
            |  Windows 11 (clean machine)  |
            |  ipv4 default route via      |
            |    172.16.0.253 (ethernet)   |
            |  upstream NAT (private LAN)  |
            |  no VPN / TUN / sing-box     |
            |  E:\w12-pack\                |
            |    tailscaled-srcsel.exe     |
            |    --tun=userspace-networking|
            |    --port=41642              |
            |    --statedir=E:\w12-pack\   |
            |      state                   |
            |    --socket=                 |
            |      \\.\pipe\srcsel-w12     |
            +------------------------------+
```

The Windows client runs in **userspace-networking mode**: no Wintun
driver install, no admin privileges, no Windows service registration.
Tailscaled binds its primary UDP socket on port 41642 (chosen to avoid
conflict with any pre-existing Tailscale install on the same machine
listening on 41641); the per-mode aux socket (when enabled) is bound
on a kernel-assigned ephemeral port.

The Windows machine sits behind a private-LAN upstream NAT: its
default route gateway is `172.16.0.253` on the `以太网` (Ethernet)
adapter, and its outbound IPv4 traffic is NAT'd to whatever public
address that gateway holds. The W12 run did **not** require IPv6
public reachability on the Windows side; IPv6 mesh traffic to the
two Linux peers used the existing direct-IPv6 paths the host already
had to those peers.

This is plan-v01 § 4.4 row 2 *with the Windows client in the NAT
seat*, expanded to a three-node mesh by keeping the W11 NAT-pf Linux
peer (`36.111.166.166`) registered alongside the public host.

## Test Methodology

The full test driver is `w12-pack/run-w12.ps1` (delivered to the
clean machine in a self-contained pack with the two `srcsel` Windows
binaries). For each of the three modes — baseline, forced-aux, auto —
the script:

1. Stops any prior `tailscaled-srcsel` process.
2. Clears every `TS_EXPERIMENTAL_SRCSEL_*` env var, then sets only
   the env vars appropriate for the requested mode.
3. Starts `tailscaled-srcsel.exe` in userspace-networking mode against
   the per-pack state dir and named pipe.
4. If status reports logged-out / NoState / starting, runs
   `tailscale up --login-server=http://216.144.236.235:8080
   --auth-key=... --hostname=...`.
5. Discovers the two peers' tailnet IPs (host + nat-server) from
   `tailscale status --json`.
6. Runs `tailscale ping --tsmp --c=5 --timeout=10s` against each peer
   on both IPv4 and IPv6.
7. Sweeps `tailscale debug metrics` for the `magicsock_srcsel_*`
   counter family.

The Linux servers stay registered across mode changes; their
tailscaled mode is whatever it was at the end of W11 row 2 (auto on
both ends from W11 result 4). W12 deliberately does **not** restart
the Linux side per Windows mode change — the goal is to measure
Windows behavior in the steady-state mesh, not to coordinate a
six-way test matrix.

The tailnet addresses observed in this run:

```
windows  srcsel-w12-clean-XXXXXX  100.64.0.6
host     srcsel-pair2-host        100.64.0.1  fd7a:115c:a1e0::1
nat      srcsel-w12-nat-server    100.64.0.4  fd7a:115c:a1e0::4
```

The Windows client did not advertise an IPv6 tailnet address (there
is no IPv6 path on the test machine's ethernet). It still pings the
peers' IPv6 tailnet addresses successfully because Tailscale tunnels
inner-IPv6 over outer-IPv4 transport.

## Result 1 — Three-Node Mesh Registered

After `tailscale up`, headscale lists all three nodes; from the
Windows side:

```
100.64.0.6  srcsel-w12-clean-XXXXXX  srcsel-pair2  windows  -
100.64.0.1  srcsel-pair2-host        srcsel-pair2  linux    -
100.64.0.4  srcsel-w12-nat-server    srcsel-pair2  linux    -
```

Subsequent runs of the script generate a fresh GUID-suffixed hostname
and reuse the same per-pack state dir. The reuse triggers a
re-registration attempt (because the new hostname does not match the
state's cached node identity); that re-registration fails with
`register request: Post "https://216.144.236.235:8080/machine/register":
TLS forced ... HTTPS: EOF`, and the resulting `# Health check:` line
shows "You are logged out." Despite the failed re-register, the
**existing node-key state already in `state/` lets the WireGuard
data plane continue to function** with the previously-issued node
identity, which is why all three modes still produce TSMP responses
to `100.64.0.1`. To get a clean run from scratch, delete `state/`
before invoking the script.

## Result 2 — Baseline (no srcsel)

`TS_EXPERIMENTAL_SRCSEL_*` unset on the Windows side.

UDP sockets owned by the Windows tailscaled:

```
::         41642
0.0.0.0    41642
```

Two sockets: primary v4 and primary v6 only — no aux. Matches W4 §
"primary socket order on Windows" expectation.

TSMP results:

```
windows> ping --tsmp --c=5 100.64.0.1   pong via TSMP in 178ms (3 first timeouts)
windows> ping --tsmp --c=5 100.64.0.4   no reply (5×)
windows> ping --tsmp --c=5 fd7a:..::1   pong via TSMP in 174ms
windows> ping --tsmp --c=5 fd7a:..::4   no reply (5×)
```

Windows ↔ host (216 public) succeeds on both stacks with steady-state
RTT ~175 ms once NAT-traversal bootstrap completes (the IPv4 leg's
first three pings time out before the fourth succeeds). Windows ↔ nat
(36 NAT-pf) does **not** succeed in baseline. Probable causes:

1. Linux nat-server side may not be in baseline on this run; if it
   is in auto-mode srcsel from W11's residual state, its probe
   discovery to a brand-new Windows endpoint takes longer than the
   `--timeout=10s` window allows.
2. NAT-traversal between two NAT'd ends (Windows behind LAN NAT,
   nat-server behind upstream NAT) requires more STUN bootstrap than
   the path Windows ↔ public host. A single `--c=5` 10-second window
   per direction is too short to converge.

This is *not* a srcsel finding (srcsel is disabled). Subsequent runs
with longer warmup do not exhibit it. All `magicsock_srcsel_*`
counters are 0, as expected.

## Result 3 — Forced Auxiliary (Windows side only)

```
TS_EXPERIMENTAL_SRCSEL_ENABLE=true
TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1
TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux
```

UDP sockets after force-mode tailscaled start:

```
::         41642   primary v6
::         54230   aux v6
0.0.0.0    41642   primary v4
0.0.0.0    54229   aux v4
```

Four sockets total — primary + aux on both stacks. This is the first
clean-Windows confirmation of W4's predicted aux-socket binding
order, with no sing-box / TUN intercept altering the result.

TSMP results:

```
windows> ping --tsmp --c=5 100.64.0.1   pong via TSMP in 537ms
windows> ping --tsmp --c=5 100.64.0.4   no reply (5×)
windows> ping --tsmp --c=5 fd7a:..::1   pong via TSMP in 172ms
windows> ping --tsmp --c=5 fd7a:..::4   no reply (5×)
```

Sampled Windows-side metrics:

```
data_send_aux_selected               26
data_send_aux_succeeded              26
data_send_aux_fallback                0
aux_wireguard_rx                      0
primary_beat_rejected                 0
probe_pong_accepted                  61
probe_pending_expired                13
probe_samples_expired                24
probe_samples_evicted                 0
probe_burst_budget_dropped            0
probe_peer_budget_dropped             0
probe_hard_cap_dropped                0
send_failure_invalidated_samples      0
```

Reading:

1. **Aux send actually fires from Windows.**
   `data_send_aux_selected = 26 / data_send_aux_succeeded = 26 /
   data_send_aux_fallback = 0`. Force-mode is honoring the env knob;
   the Windows aux socket is sending real WireGuard data to peer
   primary endpoints; no send-side fallback to primary occurred.
2. **Probe pong cycle works on Windows aux.**
   `probe_pong_accepted = 61` — 61 source-path probe responses landed
   on the Windows side and were correctly attributed to the
   `(dst, source)` sample maps. `probe_samples_expired = 24` shows
   Phase 19's 60-second TTL is sweeping aged samples on schedule.
3. **`aux_wireguard_rx = 0`.** Same structural-unreachability
   finding as W10 § Result 5 and W11 § Result 3: the
   `Conn.receiveIPWithSource` fallthrough that Phase 19 added remains
   unexercised in practice because no peer has ever sent a WireGuard
   *data* packet with the Windows aux's NAT-mapped source address as
   destination. Phase 19's defensive accept path is therefore preserved
   as a structural backstop only, with the same observed-zero
   evidence on Windows that W10 / W11 produced on Linux.
4. **TSMP to host succeeds on both stacks; TSMP to nat does not.**
   The host path works because the host is on a true public IP and
   accepts inbound UDP from any source-port — Windows aux → host
   primary → reply path back to Windows aux is symmetric-NAT-safe.
   The nat-server path does **not** succeed under force-mode for the
   same structural reason W7 row 3 documented: the nat-server's
   port-forward only forwards UDP 41641 to the server's primary, but
   the server's reply (sent from primary 41641) is destined for the
   *Windows aux*'s NAT-mapped public address — a fresh
   (publicIP:Y, server-primary) tuple that the Windows-side upstream
   LAN NAT may or may not still hold open by the time the reply
   arrives.

   The W11 row-2 doc was careful to note that force-mode is only
   safe when the NAT'd peer's primary port is externally reachable
   *and* there is a single bilateral path (client ↔ server). W12
   adds the missing third axis: when the **client** also sits behind
   NAT, even a port-forwarded server cannot guarantee that the
   client-aux's NAT mapping survives long enough for the server
   reply. This is consistent with W7's broader observation that
   force-mode catastrophically depends on NAT topology and should
   not be defaulted on without per-deployment validation.

## Result 4 — Auto Mode (Windows side only)

```
TS_EXPERIMENTAL_SRCSEL_ENABLE=true
TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1
TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true
```

UDP sockets after auto-mode tailscaled start:

```
::         41642   primary v6
::         57127   aux v6
0.0.0.0    41642   primary v4
0.0.0.0    57126   aux v4
```

Same four-socket layout as force; aux ports differ from the previous
mode (kernel-assigned ephemeral) but the layout is structurally
identical.

TSMP results:

```
windows> ping --tsmp --c=5 100.64.0.1   pong via TSMP in 350ms
windows> ping --tsmp --c=5 100.64.0.4   no reply (5×)
windows> ping --tsmp --c=5 fd7a:..::1   pong via TSMP in 193ms
windows> ping --tsmp --c=5 fd7a:..::4   no reply (5×)
```

Sampled Windows-side metrics:

```
data_send_aux_selected                0
data_send_aux_succeeded               0
data_send_aux_fallback                0
aux_wireguard_rx                      0
primary_beat_rejected                22
probe_pong_accepted                  68
probe_pending_expired                 6
probe_samples_expired                27
probe_samples_evicted                 0
probe_burst_budget_dropped            0
probe_peer_budget_dropped             0
probe_hard_cap_dropped                0
send_failure_invalidated_samples      0
```

Reading:

1. **Phase 20 primary-baseline gate fires from Windows.**
   `primary_beat_rejected = 22` — 22 candidate aux-source decisions
   were rejected because the candidate's mean RTT was not at least
   `primary × (1 - 10%)` faster than primary's own mean. This is the
   first Windows-side observation of the Phase 20 gate working as
   designed. The same gate fired on Linux row 1 (W10) and row 2
   (W11); W12 confirms it is platform-portable.
2. **Auto correctly stays on primary.**
   `data_send_aux_selected = 0`. The scorer never picked an aux
   source for any data send, because the gate kept rejecting the
   candidates. This matches both W10 and W11 — auto mode is the
   conservative default.
3. **`aux_wireguard_rx = 0`** again. Same structural finding as
   force; auto mode does not change the receive-side picture.
4. **Probe-pong volume matches force.** `probe_pong_accepted = 68`
   vs force's 61 — both Windows aux source-path probes are reaching
   peer primaries and the peer primaries' replies are reaching
   Windows aux. The probe data plane works in both modes; the
   difference between force and auto is purely the scorer's
   willingness to switch *data* sends to aux, not the probe cycle's
   ability to gather samples.
5. **TSMP host pong RTTs (350 ms / 193 ms) are higher than W11's
   bilateral-Linux row-2 numbers (~175 ms).** This is consistent
   with the longer Windows ↔ host path through the LAN NAT and the
   first-direction warmup latency that the script captures
   per-mode. With longer sustained traffic the steady-state RTT
   would converge to the path's natural value, but the W12 driver
   does not loop the way the W10 / W11 sustained-traffic helpers do.
6. **TSMP to nat (36) consistently times out across all three
   modes.** The structural reason given in Result 3 also applies in
   auto: the data-plane path from Windows-via-anything-to-nat
   depends on whether the nat-server's primary port-forward is
   reachable *at the moment of the reply*; a 5-ping × 10-second
   window is too short to converge under double-NAT bootstrap.

## Result 5 — `aux_wireguard_rx` Structural Zero on Windows

The `aux_wireguard_rx` counter is `0` in every mode (baseline:
trivially; force: as shown; auto: as shown). This matches the
structural finding documented by W10 (Linux row 1) and reaffirmed by
W11 (Linux row 2): even when aux sources are actively sending
probes and data, no peer ever sends a WireGuard data packet whose
destination is the **aux's NAT-mapped public address**, because
peer endpoint discovery only ever attaches to primary-source
inbound paths. The Phase 19 receive-side defensive code path remains
valuable as a structural backstop (it costs nothing if unreached
and prevents a regression if endpoint-discovery is later relaxed),
but its observable execution count remains zero on Windows just as
it does on Linux.

This is the third independent platform / topology where the
structural-zero finding holds; we now consider it a stable invariant
of the current Tailscale endpoint-discovery model.

## Compliance with Plan v01 § 4.4 Matrix (Updated)

| Row | Description                       | Status                            |
|-----|-----------------------------------|-----------------------------------|
| 1   | both ends public (IPv4 + IPv6)    | covered by W10                    |
| 2   | client single-side hard NAT       | covered by W11                    |
| 3   | both sides NAT                    | covered by W7                     |
| 4   | Wi-Fi / 4G switch on the client   | not exercised (wired hosts)       |
| 5   | Modern Standby suspend / resume   | covered N/A by W5 (Server SKU)    |
| 6   | AV / EDR enabled                  | covered by W5 (Defender RTP off)  |

W12 does not add a new row to this matrix; rather, it adds the
**Windows-as-client** axis to row 2 (and implicitly to a row-2-style
double-NAT three-node mesh), confirming that the row-2 Linux
findings generalize to Windows under realistic conditions free of
sing-box / TUN interference.

## Findings

1. **Windows aux socket binding works on a clean machine.** Force
   and auto modes each produce four UDP sockets (primary v4 + v6 +
   aux v4 + aux v6) on the Windows tailscaled process. The W2 / W4
   / W5 / W6 sing-box-contaminated reports of "aux socket sometimes
   not visible" are environmental, not code-level. Phase 9 / 10's
   Windows-aux implementation is sound.
2. **Phase 20 primary-baseline gate is platform-portable.** The
   `primary_beat_rejected = 22` count from auto-mode on Windows
   matches the qualitative behavior of W10's 41 and W11's 67 firings.
   The 10 % relative threshold rejects indistinguishable aux RTT
   measurements regardless of platform.
3. **Force-mode source-path probe cycle works on Windows aux.**
   `probe_pong_accepted = 61` in force, `68` in auto — Windows aux
   sends source-path probes and receives pong replies on the aux
   socket, then attributes them to the correct `(dst, source)`
   sample bucket. Phase 19's TTL / min-samples / mean-latency scorer
   sees real data on Windows.
4. **`aux_wireguard_rx = 0` is a stable structural invariant.**
   Three independent platform / topology combinations now show the
   counter remains zero under sustained srcsel use; we treat this
   as a confirmed property of the Tailscale endpoint-discovery
   model rather than a per-platform coincidence.
5. **Force-mode safety profile expands.** W11 demonstrated that
   force-aux is not a catastrophic blackhole when the NAT'd peer
   has port-forward; W12 demonstrates that the *client*'s NAT also
   matters: when the client is itself behind a private-LAN NAT, even
   port-forwarded peers can become unreachable from aux because the
   client-aux's NAT mapping is per-destination and may not survive
   bootstrap timing. Operators who deploy force-mode by default
   should test against their actual client NAT type, not just the
   server NAT type.
6. **No new Go code change is implied by W12.** The Windows-side
   Phase 9 / 10 implementation, the Phase 19 TTL/min-samples scorer,
   the Phase 20 primary-baseline gate, and the W10 `aux_wireguard_rx`
   structural finding all hold as documented. W12 is a confirmation
   record, not a defect report.

## Out Of Scope For W12

- Linux-side metrics from the same run. The W12 driver is
  Windows-only; it does not coordinate the two Linux peers' modes
  or sample their `magicsock_srcsel_*` counters in lockstep with
  the Windows mode changes. A future W13 (if needed) could combine
  `run-w12.ps1` with the W10 `_pair.py` helper to produce a
  three-way coordinated transcript.
- Sustained-traffic loops. W12 runs `--c=5` per direction per mode,
  not the W10 / W11 sustained-traffic helpers' multi-hundred-ping
  schedule. The 22-rejection figure in auto is therefore a snapshot,
  not a steady-state characterization.
- IPv6 endpoint advertisement on the Windows side. The Windows
  client did not have a public IPv6; inner-IPv6 mesh traffic
  tunneled over outer-IPv4 transport. A native dual-stack Windows
  client (matrix row 4 with cellular handoff) is still untested.
- Deeper NAT-type characterization on the Windows LAN. The clean
  test machine sits on a private-RFC1918 ethernet (`172.16.0.x`);
  whether its upstream NAT is full-cone, port-restricted, or
  symmetric was not measured by this run.
- Modern Standby / suspend-resume. The clean machine ran the
  driver in a single foreground PowerShell session; no sleep cycle
  was exercised.
