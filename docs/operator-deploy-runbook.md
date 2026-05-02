# Operator deployment runbook — fullcone/multiport srcsel

End-to-end install + configure runbook for the Tailscale srcsel fork
including Phase 21 (dynamic multi-endpoint advertise) and Phase 22 v2
(direct-vs-relay latency-aware switching).

For a one-page TL;DR of which env knob solves which problem, see
[operator-quickref.md](operator-quickref.md).

---

## 0. Prerequisites

- A control-plane host (headscale 0.28+) with a stable public IP. One
  control plane can serve unlimited clients.
- One or more **server** machines (the side with services to expose).
  May be fully public, NAT-pf'd, or behind a load-balancer.
- One or more **client** machines (Linux, Windows 10/11/Server 2019+,
  or macOS).
- Outbound reachability: TCP 8080 to control + arbitrary UDP for the
  data plane. UDP 41641 inbound on the server side if you want direct
  connections.
- Go 1.22+ on a build host (only needed to compile binaries; not on
  runtime hosts).

Reference topology used throughout this doc:

```
control     1.2.3.4        (public, runs headscale on :8080)
server-A    public-via-LB  (multi-public-IP, exercises Phase 21)
server-B    NAT-pf 41641   (single public IP, classic NAT-pf)
client-1    Linux + arbitrary NAT
client-2    Windows + arbitrary NAT
```

The minimum bring-up is 1 control + 1 server + 1 client.

---

## 1. Build the binaries

On any Linux dev host with Go installed:

```bash
git clone https://github.com/fullcone/multiport
cd multiport
git checkout main      # main is the merged + reviewed line

# Linux server / client
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
    -o build/linux/tailscaled-srcsel ./cmd/tailscaled
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
    -o build/linux/tailscale-srcsel ./cmd/tailscale

# Windows client
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
    -o build/windows/tailscaled-srcsel.exe ./cmd/tailscaled
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
    -o build/windows/tailscale-srcsel.exe ./cmd/tailscale

# Verify commit hash
./build/linux/tailscaled-srcsel --version
# tailscale commit: <main HEAD SHA>
```

Distribute:

```bash
# Linux runtime hosts
scp build/linux/tailscaled-srcsel build/linux/tailscale-srcsel \
    root@<host>:/usr/local/bin/
ssh root@<host> chmod +x /usr/local/bin/tailscale*-srcsel

# Windows runtime hosts: copy both .exe to e.g. E:\multiport-pack\
```

---

## 2. Control plane — headscale

Skip this section if you already have a headscale instance.

```bash
# On control host (assume public IP 1.2.3.4)
sudo wget -O /usr/local/bin/headscale \
    https://github.com/juanfont/headscale/releases/download/v0.28.0/headscale_0.28.0_linux_amd64
sudo chmod +x /usr/local/bin/headscale
sudo mkdir -p /etc/headscale /var/lib/headscale
```

Minimum `/etc/headscale/config.yaml`:

```yaml
server_url: http://1.2.3.4:8080
listen_addr: 0.0.0.0:8080
metrics_listen_addr: 127.0.0.1:9090
grpc_listen_addr: 127.0.0.1:50443
private_key_path: /var/lib/headscale/private.key
noise:
  private_key_path: /var/lib/headscale/noise_private.key
ip_prefixes:
  - 100.64.0.0/10
  - fd7a:115c:a1e0::/48
database:
  type: sqlite3
  sqlite:
    path: /var/lib/headscale/db.sqlite
log:
  level: info
dns:
  magic_dns: true
  base_domain: example.com
  override_local_dns: false
  nameservers:
    global:
      - 1.1.1.1
```

Run it as a service:

```bash
sudo tee /etc/systemd/system/headscale.service >/dev/null <<'EOF'
[Unit]
Description=headscale
After=network-online.target
[Service]
ExecStart=/usr/local/bin/headscale serve
Restart=always
RestartSec=3
[Install]
WantedBy=multi-user.target
EOF
sudo systemctl daemon-reload
sudo systemctl enable --now headscale

# Create user + reusable preauth key
sudo headscale users create srcsel
sudo headscale preauthkey create --user srcsel --reusable --expiration 24h
# → prints hskey-auth-XXXXXXXXXXX. Re-use this for every new node.
```

Verify:

```bash
curl -fsS http://1.2.3.4:8080/health && echo
# {"status":"pass"}
```

---

## 3. Server side (Linux) — Phase 21 enabled

Goal: tailscaled-srcsel running with srcsel `auto` + Phase 21 dynamic
multi-endpoint advertise watching a JSON file.

```bash
# Binaries already at /usr/local/bin/. Set up state + config dirs.
sudo mkdir -p /var/lib/srcsel
sudo mkdir -p /etc/tailscaled

# Phase 21: write the initial endpoint pool. Empty list is fine if you
# don't have extra public IPs yet — the watcher just won't advertise
# anything until the file gets entries.
sudo tee /etc/tailscaled/extra-endpoints.json >/dev/null <<'EOF'
{"endpoints": []}
EOF

# CRITICAL: file mode must NOT be group- or world-writable. The
# watcher refuses 0660/0666 etc on Linux/macOS.
sudo chown root:root /etc/tailscaled/extra-endpoints.json
sudo chmod 0644 /etc/tailscaled/extra-endpoints.json
ls -l /etc/tailscaled/extra-endpoints.json
# -rw-r--r-- 1 root root ... 
```

systemd unit `/etc/systemd/system/tailscaled-srcsel.service`:

```ini
[Unit]
Description=Tailscale srcsel (server)
After=network-online.target

[Service]
# === Phase 21: dynamic multi-endpoint advertise ===
Environment=TS_EXPERIMENTAL_EXTRA_ENDPOINTS_FILE=/etc/tailscaled/extra-endpoints.json
# TS_EXPERIMENTAL_EXTRA_ENDPOINTS_MAX is unset → unlimited (default).
# Set a positive integer only if you want a hard policy ceiling on what
# an upstream orchestrator can publish under this node's identity.
# Environment=TS_EXPERIMENTAL_EXTRA_ENDPOINTS_POLL_S=30
#   ↑ enable polling backup if fsnotify is unreliable (NFS, some FUSE).

# === srcsel data plane: fixed dual-send redundancy ===
Environment=TS_EXPERIMENTAL_SRCSEL_ENABLE=true
Environment=TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1
Environment=TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY=dual-send
Environment=TS_EXPERIMENTAL_SRCSEL_DUAL_SEND=true

ExecStart=/usr/local/bin/tailscaled-srcsel \
    --tun=userspace-networking \
    --socket=/tmp/srcsel.sock \
    --statedir=/var/lib/srcsel \
    --port=41641
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

Start and register:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now tailscaled-srcsel

# One-shot register the node to headscale.
sudo /usr/local/bin/tailscale-srcsel \
    --socket=/tmp/srcsel.sock up \
    --login-server=http://1.2.3.4:8080 \
    --auth-key=hskey-auth-XXXXXXXXX \
    --hostname=server-a \
    --accept-routes=false \
    --accept-dns=false \
    --unattended

sudo /usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock status
# 100.64.0.1   server-a   srcsel   linux   -
```

### 3.1 Populate Phase 21 endpoint pool

When you're ready to advertise the additional front doors:

```bash
sudo tee /etc/tailscaled/extra-endpoints.json >/dev/null <<'EOF'
{
  "endpoints": [
    "5.6.7.8:41641",
    "9.10.11.12:41641",
    "13.14.15.16:41641"
  ]
}
EOF
```

The change is detected by fsnotify within milliseconds; tailscaled then
issues a ReSTUN cycle and the new set propagates to peers via the
existing control-plane long-poll within ~1 s.

Live-tail to confirm:

```bash
sudo journalctl -u tailscaled-srcsel -f | grep -E "extra-endpoints|ReSTUN"
# magicsock: extra-endpoints: loaded 3 endpoint(s) from "..."
# magicsock: starting endpoint update (extra-endpoints-changed)
```

**Capacity**: tens of thousands of endpoints are fine.
`TS_EXPERIMENTAL_EXTRA_ENDPOINTS_MAX` is unset by default = unlimited;
the only guard is the 64 MB file-size memory ceiling, sized for a
100 000-entry baseline deployment with ~10× headroom (an IPv6
`"[v6-addr]:port"` entry is ~50–60 bytes; 64 MB ≈ 1.1 M entries far
above any plausible deployment, while still bounding memory against a
runaway / corrupt file).

### 3.2 Phase 21 verification checklist

```bash
# 1. envknobs accepted
sudo journalctl -u tailscaled-srcsel | grep envknob | head -10
# Expected lines include:
#   envknob: TS_EXPERIMENTAL_EXTRA_ENDPOINTS_FILE="..."
#   envknob: TS_EXPERIMENTAL_SRCSEL_ENABLE="true"
#   envknob: TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS="1"
#   envknob: TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY="dual-send"
#   envknob: TS_EXPERIMENTAL_SRCSEL_DUAL_SEND="true"

# 2. extra-endpoints actually loaded
sudo journalctl -u tailscaled-srcsel | grep "extra-endpoints"
#   magicsock: extra-endpoints: loaded N endpoint(s) from "..."

# 3. srcsel aux sockets bound (dual-send/force mode = 4 sockets total)
sudo ss -lunp | grep tailscaled-srcs
# UNCONN ... 0.0.0.0:41641 ← primary v4
# UNCONN ... 0.0.0.0:54xxx ← aux v4 (random ephemeral)
# UNCONN ... [::]:41641    ← primary v6
# UNCONN ... [::]:54xxx    ← aux v6

# 4. metrics
sudo /usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock debug metrics | \
    grep -E "magicsock_extra_endpoints|magicsock_srcsel"
```

---

## 4. Client side (Linux) — Phase 22 v2 enabled

```bash
sudo mkdir -p /var/lib/srcsel
```

systemd unit `/etc/systemd/system/tailscaled-srcsel.service`:

```ini
[Unit]
Description=Tailscale srcsel (client)
After=network-online.target

[Service]
# === srcsel data plane ===
Environment=TS_EXPERIMENTAL_SRCSEL_ENABLE=true
Environment=TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1
Environment=TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY=dual-send
Environment=TS_EXPERIMENTAL_SRCSEL_DUAL_SEND=true

# === Phase 22 v2: direct-vs-relay latency-aware switching ===
Environment=TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE=true
# Defaults are usually fine; uncomment and tune as needed:
# Environment=TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE_INTERVAL_S=300
# Environment=TS_EXPERIMENTAL_DIRECT_VS_RELAY_HOLD_S=300
# Environment=TS_EXPERIMENTAL_DIRECT_VS_RELAY_THRESHOLD_PCT=10

# === Alternative: hard-force all traffic through relay (replaces Phase 22) ===
# Environment=TS_DEBUG_NEVER_DIRECT_UDP=true

ExecStart=/usr/local/bin/tailscaled-srcsel \
    --tun=userspace-networking \
    --socket=/tmp/srcsel.sock \
    --statedir=/var/lib/srcsel \
    --port=41641
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

Start and register:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now tailscaled-srcsel

sudo /usr/local/bin/tailscale-srcsel \
    --socket=/tmp/srcsel.sock up \
    --login-server=http://1.2.3.4:8080 \
    --auth-key=hskey-auth-XXXXXXXXX \
    --hostname=client-1 \
    --accept-routes=false \
    --accept-dns=false \
    --unattended
```

---

## 5. Client side (Windows)

Windows clients are typically not run as services in test/dev. The
recommended pattern is a self-contained pack folder + launcher script.

### 5.1 Layout

```
E:\multiport-pack\
    tailscaled-srcsel.exe
    tailscale-srcsel.exe
    state\         (auto-created by tailscaled)
    logs\          (auto-created by launcher)
    start-srcsel.ps1
```

### 5.2 Launcher `E:\multiport-pack\start-srcsel.ps1`

```powershell
$ErrorActionPreference = "Stop"
$pack  = "E:\multiport-pack"
$state = Join-Path $pack "state"
$logs  = Join-Path $pack "logs"
New-Item -ItemType Directory -Force -Path $state, $logs | Out-Null

# === srcsel data plane (fixed dual-send redundancy) ===
$env:TS_EXPERIMENTAL_SRCSEL_ENABLE = "true"
$env:TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS = "1"
$env:TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY = "dual-send"
$env:TS_EXPERIMENTAL_SRCSEL_DUAL_SEND = "true"

# === Phase 22 v2 ===
$env:TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE = "true"
# Optional tuning:
# $env:TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE_INTERVAL_S = "60"
# $env:TS_EXPERIMENTAL_DIRECT_VS_RELAY_THRESHOLD_PCT = "5"

# Stop any prior instance before relaunching.
Get-Process tailscaled-srcsel -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Milliseconds 800

$tsd = Join-Path $pack "tailscaled-srcsel.exe"
$args = @(
    "--tun=userspace-networking",
    "--socket=\\.\pipe\srcsel",
    "--statedir=$state",
    # Use port 41642 to avoid colliding with stock Tailscale on the same box.
    "--port=41642"
)
$proc = Start-Process -FilePath $tsd -ArgumentList $args -PassThru -NoNewWindow `
    -RedirectStandardOutput "$logs\tailscaled.log" `
    -RedirectStandardError "$logs\tailscaled.err"
Write-Host "tailscaled-srcsel pid=$($proc.Id)"
Start-Sleep -Seconds 4

# First-time registration. Subsequent runs do not need this — state
# survives in $state and re-registers automatically.
$tsCli = Join-Path $pack "tailscale-srcsel.exe"
& $tsCli "--socket=\\.\pipe\srcsel" up `
    --login-server="http://1.2.3.4:8080" `
    --auth-key="hskey-auth-XXXXXXXXX" `
    --hostname=win-client-1 `
    --accept-routes=$false `
    --accept-dns=$false `
    --unattended

& $tsCli "--socket=\\.\pipe\srcsel" status
```

Run:

```powershell
PowerShell -ExecutionPolicy Bypass -File E:\multiport-pack\start-srcsel.ps1
```

To run as a Windows service for auto-start, wrap with `nssm.exe` or
similar; that's out of scope for this doc.

---

## 6. End-to-end verification

From a client:

```bash
# tailnet identity
sudo /usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock status
# 100.64.0.1  server-a   srcsel   linux   active; direct 1.2.3.4:41641, ...
# 100.64.0.2  client-1   srcsel   linux   -

# Sockets bound (auto/force = 4 per stack)
sudo ss -lunp | grep tailscaled-srcs

# Direct ping
sudo /usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock \
    ping --tsmp --c=5 100.64.0.1
# pong from server-a (100.64.0.1, 41641) via TSMP in <RTT>ms

# srcsel + Phase 21 + Phase 22 metrics in one shot
sudo /usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock debug metrics | \
    grep -E "magicsock_srcsel|magicsock_extra_endpoints|magicsock_direct_vs_relay"
```

After a few minutes of normal traffic the expected non-zero counters
include:

```
magicsock_srcsel_data_send_aux_selected     N      ← aux send fired
magicsock_srcsel_data_send_aux_succeeded    N      ← aux kernel write OK
magicsock_srcsel_aux_wireguard_rx           0      ← STRUCTURAL ZERO (expected)
magicsock_srcsel_primary_beat_rejected      M      ← Phase 20 gate firing
magicsock_srcsel_probe_pong_accepted        K
magicsock_extra_endpoints_reads             P      ← Phase 21 watcher ran (server side mostly)
magicsock_direct_vs_relay_compared          Q      ← Phase 22 saw a comparison cycle
```

---

## 7. Phase 21 hot-reload demo

Validate that operator file edits propagate to peers without restart.

```bash
# Server-A: change the pool.
sudo tee /etc/tailscaled/extra-endpoints.json >/dev/null <<'EOF'
{"endpoints": ["5.6.7.8:41641", "9.10.11.12:41641", "200.200.200.200:41641"]}
EOF

# Server-A logs (1-second window):
sudo journalctl -u tailscaled-srcsel -f | grep -E "extra-endpoints|endpoint update"
# magicsock: extra-endpoints: loaded 3 endpoint(s) ...
# magicsock: starting endpoint update (extra-endpoints-changed)

# On any peer, peer should see the new set within ~1 s:
sudo /usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock status --json | \
    jq '.Peer | to_entries[] | select(.value.HostName=="server-a") | .value.Addrs'
# Should now include "200.200.200.200:41641" alongside the others.
```

---

## 8. Phase 22 v2 swap demo

Requires at least one peer in the tailnet declaring `Hostinfo.PeerRelay = true`
(Tailscale's existing peer-relay mechanism), AND a topology where the
relay's end-to-end RTT to the destination beats direct by ≥ 10 %.

```bash
# After client-1 has been running 5+ minutes:
sudo /usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock debug metrics | \
    grep direct_vs_relay
# magicsock_direct_vs_relay_compared            N
# magicsock_direct_vs_relay_gate_relay_preferred 0  ← swap to relay never won
# magicsock_direct_vs_relay_gate_direct_preferred N
# magicsock_direct_vs_relay_hold_rejected        0

# If gate_relay_preferred stays 0 and you expect a swap, tighten the
# gate / interval and observe again over the next 5 minutes:
sudo systemctl edit tailscaled-srcsel
# Add:
#   [Service]
#   Environment=TS_EXPERIMENTAL_DIRECT_VS_RELAY_COMPARE_INTERVAL_S=60
#   Environment=TS_EXPERIMENTAL_DIRECT_VS_RELAY_THRESHOLD_PCT=5
sudo systemctl restart tailscaled-srcsel
```

---

## 9. Common pitfalls

| Symptom | Likely cause | Fix |
|---|---|---|
| `extra-endpoints: refusing to read` | File mode is group- or world-writable | `chmod 0644 /etc/tailscaled/extra-endpoints.json` |
| Edits to extra-endpoints.json don't propagate | fsnotify unreliable on this filesystem (NFS, some overlayfs) | Set `TS_EXPERIMENTAL_EXTRA_ENDPOINTS_POLL_S=30` for polling backup |
| `bind: address already in use` on tailscaled start | Old tailscaled hasn't fully released the port | Wait 2-3 s; or `pkill tailscaled-srcs` (use the bare comm name; `pkill -f tailscaled-srcsel` self-matches the killing shell — see W13 doc) |
| Phase 22 never swaps to relay | Gate too strict, no fast-enough relay candidate, or no peer with `Hostinfo.PeerRelay=true` | Lower `THRESHOLD_PCT`; verify peer relay availability via `tailscale debug netcheck`/`status` |
| Phase 22 swap flap | Hold window too short for path-RTT noise | Raise `HOLD_S` to 600 or 1800 |
| `aux_wireguard_rx` always 0 | This is **structural**, not a bug — confirmed across W10/W11/W12/W13 | Keep ignoring. If it becomes non-zero, *that's* the regression to investigate. |
| Client status shows server but ping times out | Tailnet routing established but direct UDP punch failed | DERP fallback should take over; if also failing, run `netcheck` on both ends and check NAT type compatibility |
| `register: TLS forced ...` after restart | Hostname was rotated against an existing state-dir node identity | Re-use the same hostname across restarts, or wipe `/var/lib/srcsel` to start fresh (re-registers from scratch) |

---

## 10. Upgrade procedure

```bash
# On dev/build host
cd multiport
git pull
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
    -o build/linux/tailscaled-srcsel ./cmd/tailscaled
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' \
    -o build/linux/tailscale-srcsel ./cmd/tailscale

# On each runtime host
scp build/linux/tailscale*-srcsel root@<host>:/usr/local/bin/
ssh root@<host> 'systemctl restart tailscaled-srcsel'
# Node identity preserved (state in /var/lib/srcsel); peer relationships
# resume automatically once the new tailscaled re-handshakes.
```

---

## 11. Uninstall

```bash
sudo systemctl disable --now tailscaled-srcsel
sudo rm /etc/systemd/system/tailscaled-srcsel.service
sudo rm /usr/local/bin/tailscaled-srcsel /usr/local/bin/tailscale-srcsel
sudo rm -rf /var/lib/srcsel
sudo rm -rf /etc/tailscaled        # only the srcsel-managed files

# On control plane
sudo headscale nodes list
sudo headscale nodes delete --identifier <node-id>
```

---

## 12. Where to look next

- [operator-quickref.md](operator-quickref.md) — one-page TL;DR
  cheatsheet of which env knob solves which problem.
- [tailscale-direct-multisource-udp-phasew13-three-node-coordinated-validation.md](tailscale-direct-multisource-udp-phasew13-three-node-coordinated-validation.md)
  — the most recent end-to-end network-validation transcript (Linux +
  Windows three-node mesh). Covers the asymmetric-mode "smoking gun"
  experiment that nailed down the W7 row 3 force-mode reverse-path
  blackhole at the source-port layer.
- [../ROADMAP.md](../ROADMAP.md) — Phase 21 / Phase 22 candidate
  designs (Phase 21 / 22 are now implemented; future Phase 23+ candidates
  will land here).
