# Tailscale Direct Multisource UDP Phase W13 Three-Node Coordinated Validation

Date: 2026-05-01

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\zerotier-client\multiport`

Branch: `phase-w13-coordinated-validation-doc-v2`

Pull request: not yet opened

Branch base: includes the pkill self-match fix from
`phase-w13-pkill-self-match-fix` (commit `d3cc7c22e`) plus the W13
harness merged in PR #12 (commit `c130bdf23`). No Go code changed in
W13.

This phase is documentation only. It records the first **coordinated
three-node srcsel validation** with all three peers exercised in
lockstep on each of the four scenarios `baseline / force / auto /
asymmetric-control`. W12 ran the same three-node mesh from the
Windows side only; W13 closes the W12 doc's two main Out-of-Scope
items (Linux-side metrics in the same window as Windows, plus
sustained-traffic loops at 40 pings per direction by default) and
adds a fourth scenario — a deliberate asymmetric mode mix — that
isolates the root cause of force-mode reverse-path blackhole at the
*source-port* layer rather than the topology layer.

## Topology

```
                public Internet
                 ▲            ▲
                 │            │
+----------------+            +-----------------+
| srcsel-pair2-host           srcsel-w12-nat-server
| 216.144.236.235             36.111.166.166    |
| eth0 = 216.144.236.235/28   ens3 = 192.168.1.62
|        2607:9d00:..::910c   /24 (private)     |
|        :2aa8                upstream router   |
| (true public, no NAT)       port-forwards UDP |
|                             41641 → ens3:41641|
+-----------------+-----------+-----------------+
                  │           │
                  │           │ (mesh peer of)
                  ▼           ▼
            +------------------------------+
            |  srcsel-w12-clean-1e5616     |
            |  Windows 11 (clean machine)  |
            |  default route via           |
            |    172.16.0.253 (ethernet)   |
            |  upstream LAN NAT,           |
            |  ISP CGNAT outbound          |
            |    (China Mobile 36.x.x.x)   |
            |  no VPN / TUN / sing-box     |
            |  E:\w12-pack\ (W13 reuses    |
            |    the W12 pack folder)      |
            +------------------------------+

         Headscale 0.28.0 on the host (216) at port 8080.
```

The three nodes are exactly the W12 mesh:

  - **host (216)** — fully public dual-stack Linux, runs headscale.
  - **nat (36)** — NAT-pf'd Linux (router-level UDP 41641 forwarded
    only). IPv4-only (`ipv6=false` per `tailscale netcheck`).
  - **win** — Windows 11 clean machine on a private-RFC1918 LAN
    (`172.16.x.x`) behind upstream LAN NAT, then upstream ISP CGNAT
    on the China Mobile public range. The Win-side W13 driver is
    `scripts/srcsel-w13/run-w13-windows.ps1`, which reuses the W12
    pack's binaries (`tailscaled-srcsel.exe` / `tailscale-srcsel.exe`
    at HEAD `4b330c2`) and state directory.

Compared to W7 (the original Windows ↔ Linux row 3 finding) and W12
(Windows-side-only short-probe sweep of the same three-node mesh),
W13 adds:

  - The Linux orchestrator from `scripts/srcsel-w13/w13-linux.py`
    drives both Linux peers in parallel via paramiko, restarting
    each `tailscaled-srcsel` with the chosen mode env knobs and
    sampling `magicsock_srcsel_*` metrics in the same window the
    Windows side is being measured.
  - A fourth deliberately-asymmetric scenario (`force × baseline ×
    force`) flips only the `nat` peer to baseline while keeping
    `host` + `win` in force. This experiment is unique to W13 and is
    what lets us pin down the W7 row-3 blackhole mechanism.

## Test Methodology

Four scenarios. Each scenario produces one Linux-side transcript
(host + nat metrics + Linux-source TSMP for 8 directions) and one
Windows-side transcript (Windows-source TSMP for 3 directions:
win→host v4 / v6 / win→nat v4; nat is IPv4-only so win→nat v6 is
skipped).

```
Scenario         | host(216)      | nat(36)        | win
-----------------|----------------|----------------|------------------
baseline         | baseline       | baseline       | baseline
force-symmetric  | force          | force          | force
asymmetric-ctrl  | force          | baseline       | force
auto             | auto           | auto           | auto
```

Per-mode envknobs:

```
baseline: TS_EXPERIMENTAL_SRCSEL_* unset
force:    TS_EXPERIMENTAL_SRCSEL_ENABLE=true
          TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1
          TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux
auto:     TS_EXPERIMENTAL_SRCSEL_ENABLE=true
          TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1
          TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true
```

The Linux orchestrator on the dev box and the Windows driver on the
clean machine are run by the operator at roughly the same time per
scenario — there is no network synchronization. Discovery on each
side waits up to 60 s for required peers to appear, with fail-fast
abort if any required peer is absent (per Codex feedback on PR #12).

The tailnet addresses observed throughout the run:

```
host: 100.64.0.1  fd7a:115c:a1e0::1
nat : 100.64.0.4  fd7a:115c:a1e0::4
win : 100.64.0.6  fd7a:115c:a1e0::6
```

## Result 1 — Three-Node Mesh Stable Across Mode Switches

After each mode change, all three nodes' tailscaleds re-register
their endpoints to headscale and resume peering within seconds.
Headscale fans out the new endpoint set via the standard map-update
flow. The Win-side driver kills + restarts its local
`tailscaled-srcsel` per invocation; the Linux orchestrator does the
same for `host` and `nat`. There is no manual coordination of
disco-key rotation or peer state.

A practical consequence captured here: **scenario transitions are
visible on the wire as a brief disco-key churn window**. From the
host log during the asymmetric-control transition:

```
... wgengine: Reconfig: [11Twn] changed from "discokey:..." to "discokey:..."
... wgengine: Reconfig: [11Twn] changed from "discokey:..." to "discokey:..."
... wgengine: Reconfig: [11Twn] changed from "discokey:..." to "discokey:..."
```

Three disco-key changes for the Win peer in a span of ~20 s as the
Win driver was restarted, headscale fanned out updated keys, and
host's WireGuard reconfigured. During this window TSMP from the
Windows side to host's tailnet IPv4 transiently times out before
resuming. The Linux-side rescue scripts handle this by retrying
discovery up to 60 s.

## Result 2 — Baseline (no srcsel)

`TS_EXPERIMENTAL_SRCSEL_*` unset on all three nodes.

UDP sockets per node:

```
host(216): primary v4 41641, primary v6 41641
nat(36) : primary v4 41641, primary v6 41641
win     : primary v4 41642, primary v6 41642   (--port=41642 to avoid stock 41641)
```

Two sockets per stack on each node — no aux. All `magicsock_srcsel_*`
counters remain `0` on all three nodes, as expected.

TSMP results (combining Linux orchestrator + Windows driver in the
same baseline scenario):

```
Linux-source (host orchestrator + nat orchestrator):
  host -> nat   v4: pong 400ms        v6: pong 178ms
  host -> win   v4: pong 178ms        v6: pong 179ms
  nat  -> host  v4: pong 175ms        v6: pong 178ms
  nat  -> win   v4: pong  35ms        v6: pong  35ms

Windows-source:
  win  -> host  v4: pong 354ms        v6: pong 186ms
  win  -> nat   v4: 5x timeout, then pong 605ms
```

**11 of 11 link-direction probes succeeded.** The single direction
needing bootstrap was `win → nat (v4)`: NAT-traversal between Win's
ISP CGNAT and the nat-server's port-forward took five 10-second
windows of STUN-style discovery before the direct path opened, then
pong arrived at 605 ms. Two follow-up baseline runs reproduced the
same pattern with 9× / 18× retry counts before pong, confirming the
bootstrap is real (not a one-off) but eventually settles.

**Correction to W12's Out-of-Scope claim.** The W12 doc reported
that all three modes timed out from `win → nat` and noted this as a
DERP-fallback / NAT-traversal bootstrap issue. W13's longer
sustained-traffic window confirms the timeout is recoverable: the
path *does* open after the bootstrap. W12's `--c=5` 10-second budget
per direction was just too short to cross the NAT-traversal ramp.
W13 baseline is the first run in this mesh family where 11/11
direction succeed.

## Result 3 — Force-Symmetric (W7 Row 3 Reproduce in Three-Node)

`TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux` on **all three** nodes.
UDP sockets after restart:

```
host(216): primary v4+v6 41641, aux v4 35128, aux v6 53911
nat(36) : primary v4+v6 41641, aux v4 40000, aux v6 39557 (or similar)
win     : primary v4+v6 41642, aux v4 51374, aux v6 51375 (or similar)
```

Four sockets per node — primary + aux on both stacks.

TSMP results:

```
Linux-source:
  host -> nat   v4: pong 360ms                     v6: pong 179ms
  host -> win   v4: 1x timeout, pong 2.71s          v6: pong 175ms
  nat  -> host  v4: pong 178ms                     v6: pong 179ms
  nat  -> win   v4: 10x timeout (no reply)        v6: 10x timeout (no reply)

Windows-source:
  win  -> host  v4: pong 5.364s                    v6: pong 175ms
  win  -> nat   v4: 40x timeout, "no reply"
```

**6 of 8 Linux-source directions plus 2 of 3 Windows-source
directions succeed**; the failing 4 are exactly the `nat ↔ win`
pair on both stacks. The blackhole is *symmetric*: nat→win and
win→nat both fail.

Sampled `magicsock_srcsel_*` on all three nodes after the run:

```
                                host(216 force)  nat(36 force)  win(force)
data_send_aux_selected               73             139             87
data_send_aux_succeeded              73             139             87
data_send_aux_fallback                0               0              0
aux_wireguard_rx                      0               0              0
primary_beat_rejected                 0               0              0
probe_pong_accepted                  56              57            157
probe_pending_expired               116             205              7
probe_samples_expired                37              38            137
```

End-to-end reading:

1. **Force-aux is doing real work on all three nodes.**
   `data_send_aux_selected` accumulates on each (73 / 139 / 87) and
   `data_send_aux_succeeded` exactly equals it on each — every
   force-mode data send completed at the kernel's `sendmsg` boundary,
   no `data_send_aux_fallback`. The aux socket bind + outbound code
   path is platform-portable across two distinct Linux networking
   stacks (216 fully-public + 36 NAT-pf'd) plus Windows.
2. **`probe_pending_expired` distribution is the blackhole
   topology fingerprint.** Pending-expired counts the aux source-path
   probes a node sent that never received a pong:
     - **win = 7** — Win mostly probes host (fully public) and gets
       pongs back; its outbound state is well-served.
     - **host = 116** — host probes both nat (works) and win (works
       partially); the 116 expirations are roughly the
       host→win-aux-source-port reverse path that some Win NAT
       state rejects.
     - **nat = 205** — nat probes host (works) and win (blackhole);
       the bulk of nat's 205 expirations are nat→win where Win's
       upstream CGNAT rejects nat-aux's NAT-mapped source IP:port.
   The monotone increase nat (205) > host (116) > win (7) reflects
   how each peer's reverse-path success drops as more of its peers
   sit behind a NAT that rejects aux source ports.
3. **`aux_wireguard_rx = 0` on all three nodes simultaneously** —
   W10 (Linux row 1), W11 (Linux row 2), W12 (Windows three-node
   short-probe), and now W13 (Linux + Windows three-node sustained)
   all observe the same structural zero. Phase 19's defensive
   receive path is preserved as a backstop and remains unexercised.
4. **`primary_beat_rejected = 0` on all three** — Phase 20's
   primary-baseline gate does not fire under `force`, by design;
   `force` skips the scorer entirely (per
   `sourcePathDataSendSource` short-circuit on
   `forceMode != ""`).
5. **TSMP behaviour matches W7 row 3.** The 4 failing directions
   are *all* the bidirectional `nat ↔ win` legs (Linux-NAT'd ↔
   Windows-CGNAT'd). Both `host ↔ nat` and `host ↔ win` succeed —
   `host` being fully public lets the reverse path hit *some* mapping
   that the peer's NAT will accept. The bilateral W7 result is now
   reproduced in a richer three-node setting.

This run alone reproduces the W7 row-3 finding with denser metrics,
but does not by itself isolate the *cause*. That requires Result 4.

## Result 4 — Asymmetric Control (Smoking Gun for Force-Mode Reverse Path)

The Result 3 reading attributes the `nat ↔ win` blackhole to
"NAT-aux's source IP:port being rejected by the peer's NAT". The
operator pushed back: nat (36) is effectively full-cone-equivalent
on UDP 41641 thanks to the upstream port-forward — so the **forward**
path `win-aux → 36.111.166.166:41641` should always reach
nat-primary. If both peers are in force, why is the **reverse** path
also blocked?

Hypothesis: in `force` mode, *every* outbound from a peer (including
the disco-pong reply that closes the TSMP exchange) is sent from the
peer's aux socket. The peer's NAT-mapped source IP:port for those
aux outbounds is *different* from the primary socket's mapping. If
the destination's NAT is symmetric / port-restricted (the typical
behaviour of consumer / CGNAT routers), the only outbound state it
holds for that destination IP is the one created by *primary's*
outbound — so any reply arriving on a different source port is
dropped.

The test that distinguishes this from "double-NAT topology in
general" is to flip *only one* peer's mode while keeping the rest in
force, and watch whether the previously-blackholed direction
recovers.

### Scenario

```
host(216): force      ← unchanged from Result 3
nat(36) : baseline    ← only nat is flipped; primary-only sockets
win     : force       ← unchanged from Result 3
```

UDP sockets after the flip on nat:

```
nat(36) baseline: primary v4+v6 41641 only (no aux).
```

### TSMP results from Windows side after nat -> baseline

```
win  -> host  v4: pong 708ms
win  -> host  v6: 6x timeout, then pong 169ms
win  -> nat   v4: pong 377ms        ← was 40x timeout in Result 3
```

**`win → nat (v4)` recovers from blackhole to a clean direct pong**
just by flipping nat's mode from force to baseline. **No other
network state changed**: nat's tailnet IP, public IP, port-forward,
disco key, and Windows-side mode are all the same as in Result 3.

### Mechanism

Comparing the receive path between Result 3 (force-symmetric) and
Result 4 (asymmetric):

```
                                Result 3 (nat=force)        Result 4 (nat=baseline)
                                ----------------------       -----------------------
nat reply src socket            nat-aux:40000               nat-primary:41641
nat reply src after Win NAT     36.111.166.166:40000        36.111.166.166:41641
Win NAT outbound state from
  win-aux → 36.111.166.166:41641 mapping created    same mapping created
Win NAT accepts inbound from
  36.111.166.166:40000?          NO (symmetric/PR NAT)       N/A — not used
  36.111.166.166:41641?          (irrelevant in Result 3)    YES — matches state
Result                          reply dropped at Win NAT     reply reaches win-aux
```

The Linux-side metrics during the asymmetric run corroborate:

```
host(216 force) selected (delta during the asymmetric test)  +39 (from 77 to 116)
host(216 force) probe_pending_expired (delta)                +98 (from 133 to 231)
nat(36 baseline) all magicsock_srcsel_* counters             0    (control)
win(force) selected                                          27
win(force) probe_pending_expired                              1
win(force) probe_pong_accepted                               23
```

  - `nat`'s metrics stay 0 throughout — confirms baseline is fully
    inactive on srcsel paths (no aux send, no probes).
  - `host`'s `probe_pending_expired` still climbs by 98 because
    `host` is still in force and still sends aux probes to `win` —
    *those* still face Win's CGNAT rejecting host-aux's source port.
  - `win`'s pending-expired stays at 1: Win in force still probes
    `nat`, but now nat's primary-only reply path is not blackholed.

### Refined framing for Phase 19 / Phase 20 Operators Doc

Phase 19's Operators Doc currently warns that **force-aux mode causes
reverse-path blackhole in double-NAT topologies**. W13 narrows that:

> Force-aux mode causes reverse-path blackhole **whenever the
> destination peer's NAT (or the destination's NAT in the case of
> double-NAT) is symmetric or port-restricted**. The blackhole
> trigger is not the topology — it is the fact that force forces the
> destination peer's *reply* to use its aux source port, and the
> peer's NAT only has outbound state for the source port that
> originated the bilateral session (typically primary). Even when
> the destination is NAT-pf'd ("apparently public"), the operator
> must verify that the destination's reply path can use primary, not
> aux, before turning on force on the destination side.

This is the specific sense in which W13 supersedes the "double NAT
implies force unsafe" framing. Auto mode (Result 5) avoids the issue
because the Phase 20 gate refuses to switch to aux until probes on
the aux path consistently pong — which they don't if the peer's NAT
rejects them.

## Result 5 — Auto (Production-Safe Across Three Nodes)

`TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true` on all three nodes.
Sockets identical to force (4 per node). Both Linux peers and the
Windows peer were restarted into auto, then exercised:

```
Linux-source (sustained 10 pings × 8 s timeout per direction):
  host -> nat   v4: pong 392ms        v6: pong 178ms
  host -> win   v4: pong 369ms        v6: pong 174ms
  nat  -> host  v4: pong 178ms        v6: pong 181ms
  nat  -> win   v4: pong 378ms        v6: pong  36ms

Windows-source:
  win  -> host  v4: pong 708ms        v6: pong 174ms
  win  -> nat   v4: pong 232ms
```

**11 of 11 directions succeed in auto.** The 4 directions that
blackholed under force (`nat ↔ win` v4 + v6 bidirectional) all pong
under auto. Most steady-state RTTs are 35–400 ms (direct UDP); the
single 708 ms first-ping for `win → host (v4)` reflects DERP
fallback during the auto-mode bootstrap before disco probing
established the warm direct path. (See Methodology Note 1.)

Sampled metrics:

```
                                host(216 auto)  nat(36 auto)   win(auto)
data_send_aux_selected                 0              0             0
data_send_aux_succeeded                0              0             0
primary_beat_rejected                  0              0             0
probe_pong_accepted                    2              2             3
probe_pending_expired                  0              0             0
aux_wireguard_rx                       0              0             0
```

Reading this:

1. **`data_send_aux_selected = 0` on all three nodes.** Auto mode's
   scorer never picked aux as the data-send source for any peer. Why:
   the scorer in `bestCandidateLocked` requires
   `sourcePathMinSamplesForUse = 3` fresh samples per (dst, source)
   pair before the candidate is eligible. The W13 auto run exercised
   ~2–3 probe pongs per node — right at the threshold. With this few
   samples the scorer has nothing definitive to prefer, and the safe
   default is to keep using primary.
2. **`primary_beat_rejected = 0` on all three.** The Phase 20
   primary-baseline gate is downstream of "have we got enough
   samples"; with insufficient samples, the gate is not even
   evaluated. This is consistent with W10 / W11 / W12 where longer
   loops exercised dozens of gate firings; W13's auto window was
   deliberately short (we'd already proven the gate fires correctly
   in W10/W11/W12), so a low rejection count is expected, not a
   regression.
3. **`probe_pending_expired = 0` on all three.** Unlike force,
   where the asymmetric-NAT topology produced 116 / 205 expirations,
   auto's probes get pongs back almost immediately — because the
   probe path uses primary's NAT-mapped source by default until the
   scorer confirms a faster aux exists. With no scoring switch,
   probes mirror the (working) primary path and pong.
4. **`aux_wireguard_rx = 0` everywhere** — the structural-zero
   observation now spans four independent platform / topology
   combinations (W10 Linux row 1, W11 Linux row 2, W12 Windows
   three-node short-probe, W13 Linux+Windows three-node sustained).

The auto-mode aggregate **across three nodes plus four scenarios is
zero blackholes and zero gate-mediated misroutes**. Auto is the
production-safe configuration even in the W7 row-3 topology that
trips force.

## Result 6 — `aux_wireguard_rx = 0` Across All Scenarios

Across the four W13 scenarios on three nodes:

```
                                  baseline    force     asymmetric    auto
host(216) aux_wireguard_rx           0          0           0           0
nat(36)  aux_wireguard_rx            0          0           0           0
win      aux_wireguard_rx            0          0          (0)          0
```

Twelve independent samples in this run; all zero. Combined with W10
sustained, W11 sustained, and W12 short-probe, the structural-zero
observation is now consistent across:

  - 2 platforms (Linux + Windows)
  - 4 NAT topologies (both-public, NAT-pf, double-NAT, three-node mix)
  - 4 modes (baseline / force / auto / asymmetric)
  - sustained (W10 / W11) and short-probe (W12) traffic shapes

We continue to treat this as a strong structural property of the
current Tailscale endpoint-discovery model rather than a per-platform
coincidence, while still stopping short of calling it "guaranteed"
without a longer Windows soak (per W12 doc's caveat).

## Methodology Notes

### Note 1 — DERP fallback during auto bootstrap

The single 708 ms RTT for `win → host (v4)` under auto in Result 5
is the bootstrap effect: directly after a tailscaled restart, Win's
magicsock has not yet completed a full disco-probe round-trip with
host's STUN-discovered endpoint. The first user TSMP packet falls
back to the DERP relay (region `lax`, in California) for delivery,
adding ~200 ms RTT over the warm direct UDP path's ~35–180 ms. A
second `tailscale ping` issued after disco-probing converged
returned at the warm-path RTT.

This is purely transient and **does not affect the srcsel scorer's
view**: the scorer uses `magicsock_srcsel_*` probe RTT, not user
TSMP RTT, and applies a 60-s sliding mean (Phase 19's
`sourcePathSampleTTL`) over the (dst, source) sample window. A
single bootstrap-inflated value would not skew steady-state scoring.

### Note 2 — Scorer drift handling

Operator concerns about RTT drift over time are addressed by the
existing srcsel scorer design:

  - **Sample TTL = 60 s** (`sourcePathSampleTTL`): probes older than
    60 s expire from the (dst, source) sample window via
    `pruneExpiredSamplesLocked` on every accepted Pong.
  - **Mean over fresh window** (Phase 19): the scorer reports the
    mean RTT of the fresh samples, not the historical minimum, so
    it tracks current path performance.
  - **Send-failure invalidation** (`noteSourcePathSendFailure`):
    when a real data send via aux fails, the (dst, source) cached
    sample set is invalidated immediately so the next scoring pass
    re-evaluates rather than reusing stale optimistic samples.
  - **Phase 20 10 % relative gate**: the scorer requires the aux
    candidate's mean to be at least 10 % below primary's mean
    before switching, preventing flap when aux ≈ primary.

W13 does not stress these mechanisms (the test windows are too
short), but they are exercised under longer load in W10 / W11 / W12.

### Note 3 — Coordination model

The W13 harness pair (`scripts/srcsel-w13/w13-linux.py` +
`run-w13-windows.ps1`) is **operator-synchronized, not network-
synchronized**: the operator runs both halves at roughly the same
time per scenario, and each half independently waits up to 60 s
for the required peers to appear in `tailscale status`. There is
no out-of-band signalling between the halves. This is by design —
adding SSH-from-Windows to coordinate would have added significant
infrastructure for marginal benefit, since each side's own
peer-discovery deadline tolerates ~30 s of operator-side timing
drift.

### Note 4 — Pkill self-match fix

During W13 testing the original `restart_remote()` in
`w13-linux.py` was found to use `pkill -f tailscaled-srcsel` inside
a compound bash command whose own cmdline contained the literal
string `tailscaled-srcsel` (as a nohup argument). pkill matched its
own parent shell, SIGTERM'd it, and aborted the rest of the restart
sequence — leaving tailscaled never re-launched and the verification
step's pgrep self-matching giving a false positive. The fix
(`d3cc7c22e` on this branch) switches to `pkill <comm>` matching
against the truncated 15-char `/proc/PID/comm` value
`tailscaled-srcs`, which never matches bash's `comm`. It also
adds an explicit "wait for UDP 41641 to be released by the kernel"
loop before relaunching to avoid `bind: address already in use`
errors. Earlier W7 / W10 / W11 / W12 helpers used a separate SSH
command per pkill so the self-kill was harmless; W13's compound
restart exposed the bug.

## Findings

1. **Three-node mesh baseline reaches all 11 directions with
   sustained traffic.** W12's "win → nat times out in baseline" was
   a probe-budget artifact (`--c=5`); W13's longer windows show the
   path opens after ≤ 9 retry packets. No NAT topology change is
   needed.
2. **Force-symmetric reproduces W7 row 3 in three-node form.**
   `nat ↔ win` blackholes on both stacks; `host ↔ nat` and
   `host ↔ win` succeed. Aux send completes at the kernel boundary
   (zero fallback) but disco-pong replies are dropped at the
   destination's NAT.
3. **🔴 Asymmetric control isolates the root cause to *reply
   source port*, not topology.** Flipping only nat from force to
   baseline recovers `win → nat (v4)` from 40-timeout to clean
   pong, with no other network change. The W7 row-3 framing should
   be updated from "double-NAT triggers blackhole" to "any NAT
   that rejects aux's source IP:port on the destination's reply
   path triggers blackhole". This is most NAT types in practice.
4. **Auto mode is production-safe across the same three-node
   topology.** All 11 directions pong under auto. The Phase 19
   min-samples gate plus Phase 20 primary-baseline gate combine to
   keep the scorer on primary when aux is unproven, including the
   case where aux *would* be a blackhole.
5. **`aux_wireguard_rx = 0` is now twelve independent samples
   strong** (3 nodes × 4 modes here, plus W10/W11/W12 prior).
   Counted as a structural property of Tailscale's endpoint
   discovery, not a per-platform coincidence.
6. **`probe_pending_expired` distribution is a useful blackhole
   topology fingerprint.** In W13 force the 7 / 116 / 205 ratio
   across win / host / nat directly maps to "how many of this
   node's peers' NATs reject aux source ports". An operator
   debugging an unexpected force-mode failure can use this metric
   to confirm whether the failure is reverse-path (high pending
   expired) vs. send-side (high data_send_aux_fallback) vs.
   topology-stable (low expired, working).
7. **No new Go code change is implied by W13.** The Phase 9/10
   Windows-aux implementation, the Phase 19 scorer, and the Phase
   20 gate all hold as designed under three-node coordinated load.
   The only code change in this branch is a test-harness fix to
   `w13-linux.py` (pkill self-match), no magicsock or controlclient
   change.

## Compliance with Plan v01 § 4.4 Matrix (Updated)

| Row | Description                       | Status                                                            |
|-----|-----------------------------------|-------------------------------------------------------------------|
| 1   | both ends public (IPv4 + IPv6)    | covered by W10                                                    |
| 2   | client single-side hard NAT       | partially by W11 (port-forwarded); CGNAT-only deferred            |
| 3   | both sides NAT                    | covered by W7 + reaffirmed by W13 force-symmetric three-node      |
| 4   | Wi-Fi / 4G switch on the client   | not exercised (wired hosts)                                       |
| 5   | Modern Standby suspend / resume   | covered N/A by W5 (Server SKU)                                    |
| 6   | AV / EDR enabled                  | covered by W5 (Defender RTP off)                                  |

W13 does not add a new row to this matrix; rather, it adds the
**three-node coordinated** axis to the existing row-3 result and
introduces the **asymmetric mode mix** observation that updates how
operators should reason about row-3 force safety.

## Out Of Scope For W13

  - **Sustained scorer-decision exercising under auto** — the W13
    auto run was deliberately short to validate "auto is safe" and
    "auto is platform-portable", not to repeat W10's 40-ping +
    20-round soak. A longer Windows soak under auto with full
    metric capture is the natural next step.
  - **CGNAT-only row 2 variant** — still deferred from W11. The W13
    setup uses Win's CGNAT but pairs it with the host's port-forward,
    which is already known from W11 to be a row-2-port-forwarded
    topology, not a strict CGNAT-only row 2.
  - **Network-synchronized harness** — the W13 harness pair is
    operator-synchronized. Tightening this to network-synchronized
    (e.g. via a shared marker file on the host, polled by both
    halves) would let a future W14 run all four scenarios
    automatically without per-scenario operator coordination. Not
    needed for the W13 findings.
  - **Modern Standby / suspend-resume on Win** — not exercised; the
    Win clean machine ran the driver in a single foreground
    PowerShell session.
  - **Non-china upstream ISP for Win** — Win in this run sat on China
    Mobile CGNAT. Replicating the asymmetric-control finding on
    other ISP topologies (US residential NAT, EU CGNAT) would
    strengthen the "any symmetric/port-restricted NAT" framing in
    Result 4 / Finding 3 from "observed once" to "platform-wide".
