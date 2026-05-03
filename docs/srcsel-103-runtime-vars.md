# SrcSel 103 Runtime Variables

Snapshot date: 2026-05-03.

This document records the current 103 control/node deployment and the Windows
test-client defaults. It intentionally does not record secret values such as
Headscale preauth keys, SSH private keys, or screenshot signing secrets.

## 103 Headscale

Host: `103.236.93.57`

Systemd unit:

```text
headscale.service
ExecStart=/usr/bin/headscale serve
User=headscale
Group=headscale
WorkingDirectory=/var/lib/headscale
```

`systemctl show headscale.service -p Environment --value` is currently empty;
the runtime values come from `/etc/headscale/config.yaml`.

Current non-secret Headscale config from `/etc/headscale/config.yaml`:

```text
server_url: http://103.236.93.57:2026
listen_addr: 0.0.0.0:2026
metrics_listen_addr: 127.0.0.1:9090
grpc_listen_addr: 127.0.0.1:50443
prefixes:
  magic_dns: true
  base_domain: multiport.local
unix_socket: /var/run/headscale/headscale.sock
randomize_client_port: false
```

Useful commands on 103:

```bash
systemctl status headscale --no-pager
systemctl cat headscale
grep -E '^(server_url|listen_addr|metrics_listen_addr|grpc_listen_addr|unix_socket|randomize_client_port|prefixes:|  magic_dns:|  base_domain:)' /etc/headscale/config.yaml
headscale preauthkeys create --user 1 --reusable=false --expiration 2h
```

## 103 SrcSel Node

Systemd unit:

```text
tailscaled-srcsel.service
ExecStart=/usr/local/bin/tailscaled-srcsel --tun=userspace-networking --socket=/tmp/srcsel.sock --statedir=/var/lib/srcsel --port=41641
```

Current intended environment:

```text
TS_EXPERIMENTAL_SRCSEL_ENABLE=true
TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=2
TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY=dual-send
TS_EXPERIMENTAL_SRCSEL_DUAL_SEND=true
TS_EXPERIMENTAL_EXTRA_ENDPOINTS_FILE=/etc/tailscaled/extra-endpoints.json
TS_EXPERIMENTAL_SRCSEL_PROBE_INTERVAL_MS=1000
TS_EXPERIMENTAL_SRCSEL_MAX_PROBE_BURST_GLOBAL=200
TS_EXPERIMENTAL_SRCSEL_DUAL_SEND_MAX_SKEW_MS=0
TS_EXPERIMENTAL_OMIT_ENDPOINTS=103.236.93.57:41641
```

Meaning of the probe caps:

```text
TS_EXPERIMENTAL_SRCSEL_MAX_PROBE_BURST
  Per-peer pending source-path probe cap. The 103 service currently leaves this
  unset, so it uses the default derived from AUX_SOCKETS.

TS_EXPERIMENTAL_SRCSEL_MAX_PROBE_BURST_GLOBAL
  Per-process global pending/tick source-path probe cap across all peers.
  Keep this set when a client may connect to many nodes, otherwise per-peer
  probe bursts multiply by peer count.
```

Endpoint input:

```text
/etc/tailscaled/extra-endpoints.json
```

The file is populated from the 103 endpoint collector/NAT exposure path and is
read by `tailscaled-srcsel` through `TS_EXPERIMENTAL_EXTRA_ENDPOINTS_FILE`.

Useful commands on 103:

```bash
systemctl status tailscaled-srcsel --no-pager
systemctl show tailscaled-srcsel.service -p Environment --value
journalctl -u tailscaled-srcsel -n 200 --no-pager
/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock status
/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock debug metrics | grep -E 'magicsock_srcsel_(dual|probe|local_path|remote_path)'
```

## Windows Client Pack

Local test pack path:

```text
C:\other_project\zerotier-client\client-103-tun-pack
```

Main scripts:

```text
start-client.ps1       normal start, uses screenshot auth unless a test key exists
start-client-test.ps1  test start, creates a one-time Headscale key over SSH first
new-authkey.ps1        SSH wrapper for headscale preauthkeys create
show-srcsel-paths.ps1  active/standby path view
collect-longrun.ps1    periodic status/metrics collector
stop-client.ps1        stop test client
```

Client Tailscale/SrcSel defaults from `start-client.ps1`:

```text
TS_EXPERIMENTAL_SRCSEL_ENABLE=true
TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=32
TS_EXPERIMENTAL_SRCSEL_DATA_STRATEGY=dual-send
TS_EXPERIMENTAL_SRCSEL_DUAL_SEND=true
TS_EXPERIMENTAL_SRCSEL_DUAL_SEND_MAX_SKEW_MS=0
TS_EXPERIMENTAL_SRCSEL_MAX_PROBE_BURST=50
TS_EXPERIMENTAL_SRCSEL_MAX_PROBE_BURST_GLOBAL=200
TS_EXPERIMENTAL_SRCSEL_PROBE_INTERVAL_MS=1000
TS_EXPERIMENTAL_SRCSEL_PIN_WINDOWS_UNATTENDED_PROFILE=true
TS_EXPERIMENTAL_OMIT_ENDPOINTS=103.236.93.57:41641
```

Client auth inputs:

```text
SRCSEL_AUTH_URL
  Defaults to https://monitor.wss.antiddos.cspt.fullcone.cn/api/srcsel/headscale-preauth

SRCSEL_LOGIN_SERVER
  Defaults to http://103.236.93.57:2026

SRCSEL_STEAM_ID or steam_id.txt
  Normal screenshot-auth identifier. The screenshot side should check
  online_users.steam_id plus recent heartbeat before issuing a key.

SRCSEL_AUTH_SIGN_SECRET
  Screenshot API signing secret. Do not store it in this repo.

SRCSEL_AUTH_KEY or auth_key.txt
  Manual test-only one-time Headscale key. If present, screenshot auth is
  bypassed.

SRCSEL_HEADSCALE_SSH_TARGET
  Defaults to root@103.236.93.57 for start-client-test.ps1.

SRCSEL_HEADSCALE_SSH_KEY or headscale_ssh_key
  Test-only SSH private key location. Do not distribute outside the trusted
  test environment.

SRCSEL_HEADSCALE_USER
  Defaults to Headscale user 1.

SRCSEL_HEADSCALE_AUTH_EXPIRATION
  Defaults to 2h.
```

Common Windows commands while the client is running:

```powershell
cd C:\other_project\zerotier-client\client-103-tun-pack

PowerShell -ExecutionPolicy Bypass -File .\show-srcsel-paths.ps1
PowerShell -ExecutionPolicy Bypass -File .\show-srcsel-paths.ps1 -Watch -IntervalSeconds 10

.\tailscale-srcsel.exe --socket=\\.\pipe\srcsel-103 status
.\tailscale-srcsel.exe --socket=\\.\pipe\srcsel-103 ping --tsmp --c=5 100.64.0.1
.\tailscale-srcsel.exe --socket=\\.\pipe\srcsel-103 debug metrics |
  Select-String "magicsock_srcsel_probe|magicsock_srcsel_dual|magicsock_srcsel_local_path|magicsock_srcsel_remote_path"
```

## Deployment Notes

After rebuilding new binaries from `multiport`, copy them into the Windows pack
and install the Linux binaries on 103:

```powershell
go build -trimpath -ldflags='-s -w' -o C:\other_project\zerotier-client\client-103-tun-pack\tailscale-srcsel.exe .\cmd\tailscale
go build -trimpath -ldflags='-s -w' -o C:\other_project\zerotier-client\client-103-tun-pack\tailscaled-srcsel.exe .\cmd\tailscaled

$env:GOOS="linux"; $env:GOARCH="amd64"
go build -trimpath -ldflags='-s -w' -o build\linux-amd64\tailscale-srcsel .\cmd\tailscale
go build -trimpath -ldflags='-s -w' -o build\linux-amd64\tailscaled-srcsel .\cmd\tailscaled
Remove-Item Env:GOOS,Env:GOARCH

scp build\linux-amd64\tailscale-srcsel build\linux-amd64\tailscaled-srcsel 103:/tmp/
ssh 103 "install -m 0755 /tmp/tailscale-srcsel /usr/local/bin/tailscale-srcsel && install -m 0755 /tmp/tailscaled-srcsel /usr/local/bin/tailscaled-srcsel && systemctl daemon-reload && systemctl restart tailscaled-srcsel && systemctl status tailscaled-srcsel --no-pager"
```
