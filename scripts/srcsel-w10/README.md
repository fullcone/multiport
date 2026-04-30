# srcsel W10 both-public Linux-pair helpers

Best-effort, one-time helpers used to produce the evidence in
[`docs/tailscale-direct-multisource-udp-phasew10-linux-public-pair-validation.md`](../../docs/tailscale-direct-multisource-udp-phasew10-linux-public-pair-validation.md).
They orchestrate a Linux ↔ Linux W10 run between two public-IP
dual-stack VPS hosts, with one side running headscale 0.28.0 on
`0.0.0.0:8080` and the other joining over public IPv4.

> These scripts are **not maintained tooling**. They reflect the
> commands that produced the W10 evidence; expect to tweak them for
> any other environment.

## Prerequisites

- Two public-IP Linux hosts (Ubuntu 24.04 confirmed; root SSH access
  via password or key).
- Linux binaries built locally and dropped under `<repo>/.w7-bins/`
  (W10 reuses the W7 build conventions).

## Connection config (env vars)

The headscale-running side uses the W7-style `SRCSEL_W7_*` vars; the
joining side uses parallel `SRCSEL_W10_CLIENT_*` vars:

| Env var | Purpose |
| --- | --- |
| `SRCSEL_W7_HOST` | host server IP / hostname (required) |
| `SRCSEL_W7_USER` | host SSH user (default `root`) |
| `SRCSEL_W7_PASS` | host SSH password (or) |
| `SRCSEL_W7_KEY` | absolute path to host private key |
| `SRCSEL_W7_AUTH_KEY` | headscale preauth key from step 03 |
| `SRCSEL_W10_CLIENT_HOST` | client server IP / hostname (required) |
| `SRCSEL_W10_CLIENT_USER` | client SSH user (default `root`) |
| `SRCSEL_W10_CLIENT_PASS` | client SSH password (or) |
| `SRCSEL_W10_CLIENT_KEY` | absolute path to client private key |

Mode-switch and first-run knobs (read by `04-both-up.py`):

| Env var | Default | Effect |
| --- | --- | --- |
| `SRCSEL_W10_MODE` | `baseline` | also: `force`, `auto` |
| `SRCSEL_W10_FIRST_RUN` | unset | non-empty triggers `tailscale up` |

`SRCSEL_W7_BIN_DIR` (used by `02-upload-binaries.py`) defaults to
`<repo>/.w7-bins`.

## Pipeline

```
01-recon.py             # read-only OS / NIC / public-reach probe of both hosts
02-upload-binaries.py   # sftp Linux binaries to /usr/local/bin/ on both
03-headscale-setup.py   # install + reconfigure headscale on host (0.0.0.0:8080)
                        # capture the printed hskey-auth-... into SRCSEL_W7_AUTH_KEY
04-both-up.py           # restart tailscaled on both with mode env; first run also
                        # invokes `tailscale up`
05-tsmp-test.py         # bidirectional TSMP IPv4 + IPv6 + metrics from both
06-sustained-ping.py    # 20-round sustained ping from host (exercises Phase 20)
```

`SRCSEL_W10_MODE` on `04-both-up.py` selects the three runs the W10
phase doc records:

- `baseline` — no `TS_EXPERIMENTAL_SRCSEL_*` env vars.
- `force` — `TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux`.
- `auto` — `TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true`.

(All three keep `TS_EXPERIMENTAL_SRCSEL_ENABLE=true` and
`TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1` for the srcsel runs.)

## Example end-to-end run

```powershell
# Windows PowerShell. Linux shell would use export instead of $env:.
$env:SRCSEL_W7_HOST          = "1.2.3.4"
$env:SRCSEL_W7_PASS          = "<host password>"
$env:SRCSEL_W10_CLIENT_HOST  = "5.6.7.8"
$env:SRCSEL_W10_CLIENT_PASS  = "<client password>"

cd <repo>/scripts/srcsel-w10
python 01-recon.py
python 02-upload-binaries.py
python 03-headscale-setup.py
# capture the printed hskey-auth-... value:
$env:SRCSEL_W7_AUTH_KEY = "hskey-auth-..."

# --- baseline (no srcsel) -------------------------------------------------
$env:SRCSEL_W10_MODE = "baseline"; $env:SRCSEL_W10_FIRST_RUN = "1"
python 04-both-up.py
Remove-Item Env:\SRCSEL_W10_FIRST_RUN
$env:LABEL = "baseline"
python 05-tsmp-test.py

# --- forced-aux mode ------------------------------------------------------
$env:SRCSEL_W10_MODE = "force"
python 04-both-up.py
$env:LABEL = "force"
python 05-tsmp-test.py

# --- automatic mode -------------------------------------------------------
$env:SRCSEL_W10_MODE = "auto"
python 04-both-up.py
$env:LABEL = "auto"
python 05-tsmp-test.py
python 06-sustained-ping.py
```
