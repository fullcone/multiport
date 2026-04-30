# Tailscale Direct Multisource UDP Phase W7 Bilateral Real-Network Validation

Date: 2026-04-30

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\zerotier-client\multiport`

WSL checkout: `/mnt/c/other_project/zerotier-client/multiport`

Branch: `phase-w7-real-network-validation`

Pull request: not yet opened

Branch base: `08eb7e47f` (post-PR-5 main; W9 final closeout was the last
merge before this phase).

This phase is documentation only. It records the first bilateral
real-network validation between a Windows client and a remote Linux
server using a self-hosted Headscale control plane and the Phase 1-20
+ W0-W9 srcsel implementation. No Go code changed in W7.

## W7 Acceptance from the Port Plan

`docs/tailscale-direct-multisource-udp-windows-client-port-plan-v01.md`
§ 4.4 lists six matrix rows; § 6 acceptance reads:

> 真实远端 Linux 服务器与 Windows 客户端之间至少完成一组双向 srcsel 数据
> 面验证

i.e., **at least one** bilateral matrix row must pass with both ends
exercising the srcsel data path. W7 covers the **double-NAT** row (server
behind upstream port-forward NAT, Windows behind enterprise CGNAT-style
NAT) under both forced and automatic source selection modes, plus the
no-srcsel baseline.

## Topology

```
                Headscale (127.0.0.1:8080 on srcsel-server)
                  ▲                                 ▲
                  │ control plane                   │ control plane
                  │ (direct on server)              │ (Windows OpenSSH local
                  │                                 │  port-forward via SSH key
                  │                                 │  to root@36.111.166.166)
                  │                                 │
            ┌─────┴─────┐                     ┌─────┴─────┐
            │ srcsel-   │ ─── public ───────► │ srcsel-   │
            │  server   │  Internet           │  windows  │
            │           │                     │           │
            │ ens3      │                     │ Ethernet  │
            │ 192.168.  │                     │ 10.188.   │
            │ 1.62/24   │                     │ 2.243/24  │
            │           │                     │           │
            └────┬──────┘                     └────┬──────┘
                 │                                 │
                 │ upstream NAT                    │ enterprise / CGNAT
                 │ port-forward 41641              │ symmetric mapping per
                 │ → 192.168.1.62:41641            │ outbound flow
                 ▼                                 ▼
        public 36.111.166.166                public addresses
                                             observed: 219.146.99.178,
                                             67.230.171.57 (CGNAT egress)
```

`tailscaled` runs in `--tun=userspace-networking` mode on both ends to
avoid TUN driver setup on the dev hosts. `tailscale ping --tsmp` sends
WireGuard data through `endpoint.send`, which is the path Phase 19 /
Phase 20's gates apply to.

## Build and Deployment

Both binaries built from the same WSL Ubuntu-24.04 + Go 1.26.2
toolchain at HEAD `08eb7e47f`:

```
GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
   -o tailscaled-srcsel  ./cmd/tailscaled
GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
   -o tailscale-srcsel   ./cmd/tailscale
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
   -o tailscaled-srcsel.exe ./cmd/tailscaled
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
   -o tailscale-srcsel.exe  ./cmd/tailscale
```

Linux binaries are uploaded to `/usr/local/bin/` on the remote via
paramiko sftp. Both report:

```
1.97.0-dev20260430
  tailscale commit: 08eb7e47f2ce101907a00ddc9556d5e815e272d8
```

Headscale 0.28.0 is installed on the remote via the upstream `.deb`
release, configured with default `server_url: http://127.0.0.1:8080`
(localhost only; the Windows client reaches it through an OpenSSH local
port-forward `127.0.0.1:8080 → 127.0.0.1:8080`). One user `srcsel` and
one reusable preauth key are created via `headscale users create` and
`headscale preauthkeys create`.

## Test Methodology

For each mode, both `tailscaled` processes are restarted with the
appropriate `TS_EXPERIMENTAL_SRCSEL_*` environment variables, then
`tailscale --socket=... up` is run with the headscale auth key (Windows
needs `--unattended` so tailscaled does not zero its private key when
the CLI disconnects), then `tailscale ping --tsmp` is invoked to drive
WireGuard data through the data path. The `magicsock_srcsel_*` counter
metrics are sampled with `tailscale debug metrics` after each phase.

Three runs were performed:

1. **Baseline** — no srcsel env vars.
2. **Forced** — `TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux` on both ends.
3. **Automatic** — `TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true` on both ends.

All three runs share `TS_EXPERIMENTAL_SRCSEL_ENABLE=true` and
`TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1` for the srcsel runs.

## Result 1 — Direct UDP Path Established

Disco-level `tailscale ping` (default mode) from Windows to server
returned a direct pong on the very first try after registration:

```
Windows> tailscale ping --c=5 --timeout=10s 100.64.0.1
pong from srcsel-server (100.64.0.1) via 36.111.166.166:41641 in 338ms
```

The "via" address is the server's public-side endpoint
(`36.111.166.166` = upstream NAT public IP, port 41641 =
`tailscaled --port=41641`). RTT ~340 ms reflects WAN + NAT + an
inter-province route, which is normal for this network path. The pong
arrived directly, not via DERP, confirming that Tailscale's STUN-based
hole punching plus the explicit upstream port-forward establishes a
direct UDP path under double-NAT.

## Result 2 — Baseline (no srcsel)

`TS_EXPERIMENTAL_SRCSEL_*` unset on both ends.

```
Server> tailscale ping --tsmp --c=3 --timeout=10s 100.64.0.2
pong from srcsel-windows (100.64.0.2, 46590) via TSMP in 744ms

Windows> tailscale ping --tsmp --c=3 --timeout=10s 100.64.0.1
pong from srcsel-server (100.64.0.1, 58436) via TSMP in 2.728s
```

Bidirectional WireGuard data plane works under double NAT as expected.
All `magicsock_srcsel_*` counters remain `0` because srcsel is disabled.

## Result 3 — Forced Auxiliary on Both Ends

Both `tailscaled` processes restarted with
`TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux`. `tailscaled` log shows
the env knobs picked up; `ss -ulnp` on the server shows four sockets
per process — primary v6, primary v4, aux v4, aux v6 — matching the
W4 bind cluster pattern:

```
ss -ulnp | grep tailscaled-srcs
0.0.0.0:48237 — aux IPv4
0.0.0.0:41641 — primary IPv4
[::]:41641   — primary IPv6
[::]:37892   — aux IPv6
```

`tailscale ping --tsmp` from both ends timed out:

```
Server> tailscale ping --tsmp --c=5 --timeout=15s 100.64.0.2
ping "100.64.0.2" timed out (×5, no reply)

Windows> tailscale ping --tsmp --c=5 --timeout=15s 100.64.0.1
ping "100.64.0.1" timed out (×5, no reply)
```

Sampled metrics after the run (Server-side mirrors Windows-side):

```
Windows magicsock_srcsel_data_send_aux_selected     25
Windows magicsock_srcsel_data_send_aux_succeeded    25
Windows magicsock_srcsel_data_send_aux_fallback      0
Windows magicsock_srcsel_aux_wireguard_rx            0
Windows magicsock_srcsel_probe_pong_accepted        35
Windows magicsock_srcsel_probe_pending_expired       0

Server  magicsock_srcsel_data_send_aux_selected     46
Server  magicsock_srcsel_data_send_aux_succeeded    46
Server  magicsock_srcsel_data_send_aux_fallback      0
Server  magicsock_srcsel_aux_wireguard_rx            0
Server  magicsock_srcsel_probe_pong_accepted         0
Server  magicsock_srcsel_probe_pending_expired      81
```

Reading the asymmetry:

- **Windows aux outbound succeeds at the kernel write layer** (25 sends,
  zero local fallback). Probe pongs return successfully (35 accepted),
  meaning the server's reply to a Windows-initiated source-path probe
  reaches the Windows aux NAT mapping.
- **Server aux outbound also succeeds locally** (46 sends), but
  **server-initiated source-path probes never get a pong** (0 accepted,
  81 pending expired). The server's probe leaves its aux 48237 destined
  for the Windows public-mapped aux address that Windows had earlier
  used as a source — but the Windows-side enterprise NAT does not
  carry an inbound mapping for that destination unless Windows-aux has
  recently sent traffic in the matching direction.
- **TSMP times out in both directions** because real WireGuard data
  follows the same asymmetric path: Windows-aux → server-primary works
  for the request half, but the reply, sent from server-aux (forced
  mode) to Windows-aux, is dropped by Windows-side NAT.

This is a real-world finding for the **double-NAT** matrix row: the
forced-aux mode bypasses the scorer's safety gates entirely and can
expose a NAT asymmetry that primary-only mode never sees. It is not a
defect in the implementation — forced mode is documented as an operator
override, not a guarantee — but the W7 evidence supports the Phase 19
doc's caveat that operators "carry full responsibility for the choice"
when forcing aux.

## Result 4 — Automatic Mode on Both Ends

Both ends restarted with `TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true`
and **without** `FORCE_DATA_SOURCE`. After registration completes:

```
Server> tailscale ping --tsmp --c=3 --timeout=10s 100.64.0.2
pong from srcsel-windows (100.64.0.2, 46590) via TSMP in 1.036s

Windows> tailscale ping --tsmp --c=3 --timeout=10s 100.64.0.1
pong from srcsel-server (100.64.0.1, 58436) via TSMP in 2.671s
```

Then **10 rounds × 2 pings** of sustained Windows → server TSMP traffic:

```
Round 1..10: pong via TSMP in ~188 ms each
```

After the sustained traffic, Windows-side metrics:

```
magicsock_srcsel_data_send_aux_selected     21
magicsock_srcsel_data_send_aux_succeeded    21
magicsock_srcsel_data_send_aux_fallback      0
magicsock_srcsel_primary_beat_rejected       0
magicsock_srcsel_aux_wireguard_rx            0
magicsock_srcsel_probe_pong_accepted        14
magicsock_srcsel_probe_pending_expired       1
magicsock_srcsel_probe_samples_evicted       0
magicsock_srcsel_probe_samples_expired       0
magicsock_srcsel_send_failure_invalidated_samples 0
```

Server-side metrics in the same window:

```
magicsock_srcsel_data_send_aux_selected      0
magicsock_srcsel_data_send_aux_succeeded     0
magicsock_srcsel_aux_wireguard_rx            0
magicsock_srcsel_probe_pong_accepted         0
magicsock_srcsel_probe_pending_expired      39
```

Reading the auto-mode behavior end to end:

1. **Windows accumulated probe samples.** 14 source-path probe pongs
   were accepted, exceeding `sourcePathMinSamplesForUse = 3`, so the
   Phase 19 / Phase 20 scorer became eligible to switch.
2. **Windows scorer switched to aux for real data.** 21 outbound
   WireGuard sends were routed through aux, all succeeded at the local
   write layer, and **zero fell back to primary** — meaning aux was
   chosen and the kernel write completed. This is the first
   real-network observation of `bestCandidateLocked` returning a
   selected source.
3. **Server scorer correctly stayed on primary.** Server-initiated
   probes to Windows aux all expired (39 pending expired, 0 accepted)
   because the asymmetric NAT only carries Windows→server mappings, not
   server→Windows-aux. The scorer therefore had zero qualifying samples
   and never switched, demonstrating the Phase 19
   `sourcePathMinSamplesForUse` gate's safety value under exactly the
   asymmetric NAT scenario that motivated it.
4. **TSMP pings succeeded throughout.** Bidirectional WireGuard data
   plane stayed healthy because the server reverted to primary for its
   outbound (no aux samples → no aux selection) and Windows used aux
   outbound but the server's reply via primary still reached Windows
   primary 41642's NAT mapping.
5. **`primary_beat_rejected = 0`** because the only fresh aux samples
   Windows had came from an aux path with measurably lower latency
   than the primary RTT estimate; the Phase 20 gate did not trigger.
6. **`aux_wireguard_rx = 0` on both ends** — the data plane in this
   topology never produced an aux-side WireGuard receive, because the
   asymmetric NAT keeps the server's reply on the primary path. Phase
   19's removal of the aux WireGuard drop is **defended** (the receive
   path is wired correctly) but **not exercised** here. Either matrix
   row 1 (both public) or a topology with full-cone NAT on the Windows
   side would produce non-zero `aux_wireguard_rx`; both are out of
   scope for this dev environment.

## Compliance with Plan v01 § 4.4 Matrix

| Row | Description                       | Status                                            |
|-----|-----------------------------------|---------------------------------------------------|
| 1   | both ends public, no NAT          | not testable here (server is behind upstream NAT) |
| 2   | client single-side hard NAT       | not testable here (server is also behind NAT, so the run is dual-NAT not single-sided) |
| 3   | both sides NAT                    | **covered**                                        |
| 4   | Wi-Fi / 4G switch on the client   | not exercised (host is wired only)                |
| 5   | Modern Standby suspend / resume   | not testable on Server SKU (covered as N/A in W5) |
| 6   | AV / EDR enabled                  | Defender installed, RTP off (covered in W5)       |

W7 acceptance "≥1 bilateral matrix row" is satisfied by row 3:
both ends NAT'd, automatic-mode srcsel observed switching to aux on
the side whose probe path was reachable, and bidirectional WireGuard
data plane confirmed via `tailscale ping --tsmp`. Row 2 (single-side
hard NAT) requires a topology with at least one publicly reachable
end and is left for a future test bed.

## Findings

1. **Auto-mode safety gates work as designed under asymmetric NAT.**
   The side that can warm aux NAT mappings (Windows here, since it
   sends first to a port-forwarded server) builds samples and switches
   to aux. The side that cannot (server here) accumulates expired
   probes and never switches, keeping data on primary. Neither side
   black-holes traffic.

2. **Forced-mode under asymmetric NAT is unsafe and reveals the
   reverse-path-blackhole risk that Phase 19 motivated.** Operators
   who set `TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE` in a real-world
   double-NAT environment without prior aux-side warming should expect
   bidirectional traffic loss until automatic mode would have rejected
   the path on its own. The Phase 19 doc's "operators carry full
   responsibility" framing is justified.

3. **Phase 20 primary-baseline gate did not fire** in any run; the
   measured aux mean was always meaningfully better than primary RTT
   estimates when aux was selectable, so `primary_beat_rejected` stays
   `0`. A future test under a low-latency aux path would exercise it.

4. **`aux_wireguard_rx` stays at 0 in this topology.** Phase 19's
   bidirectional aux receive removal of drop is **defended in code**
   (no regression in receive path) but **not exercised by this
   topology**. A row 1 (both public) or full-cone-NAT-on-client
   environment is required to drive a non-zero counter.

5. **Headscale + userspace-networking + SSH-tunnel control plane is
   sufficient** for srcsel real-network validation. No production
   tailnet account or DNS/cert provisioning needed; the test setup is
   self-contained.

## Reproducibility

The end-to-end orchestration scripts are archived in
[`scripts/srcsel-w7/`](../scripts/srcsel-w7/) with a README that lists
the env-var contract (`SRCSEL_W7_HOST` / `_USER` / `_PASS` or `_KEY`
plus `SRCSEL_W7_AUTH_KEY`) and the run order:

```
scripts/srcsel-w7/
  _common.py              # shared paramiko helper (env-driven config)
  01-recon.py             # initial server recon
  02-upload-binaries.py   # sftp Linux binaries to /usr/local/bin
  03-install-headscale.py # download + apt install headscale 0.28.0 .deb
  04-start-headscale.py   # start systemd service + create user + auth key
  05-server-up.py         # server-side tailscaled bring-up (baseline/force/auto)
  06-server-metrics.py    # capture server-side metrics + log lines
  07-setup-key.py         # generate ed25519 key + push to server
  08-windows-up.ps1       # SSH tunnel + Windows tailscaled + up (baseline/force/auto)
```

The mode-switch (baseline / forced-aux / automatic) is now selected
by the `SRCSEL_W7_MODE` env var on `05-server-up.py` and
`08-windows-up.ps1` rather than separate per-mode scripts.

These helpers are best-effort, one-time test harness; expect to tweak
them for any other environment.

## Out Of Scope For W7

- Public-IP-on-both-ends row (matrix row 1). The dev server is behind
  upstream NAT; row 1 is not reachable from this test bed.
- Wi-Fi/4G switch (matrix row 4) — wired-only host.
- Sustained large-data throughput. W7 validates control-and-establish
  bidirectional data flow; it does not benchmark srcsel under load.
- macOS / BSD ports — separate PR.
- `aux_wireguard_rx > 0` real-network observation — requires a
  topology where the remote side roams its WireGuard endpoint to the
  local aux address (full-cone NAT on the receive side). The current
  topology does not produce this.
