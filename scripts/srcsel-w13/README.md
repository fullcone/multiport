# srcsel W13 — three-node coordinated validation scripts

W13 reuses the W12 mesh:

  - **Windows clean machine**  (client, srcsel-w12-clean-XXXXXX)
  - **216.144.236.235**         (public dual-stack host, srcsel-pair2-host)
  - **36.111.166.166**          (NAT-pf, IPv4-only, srcsel-w12-nat-server)

Where W12 only ran the script from the Windows side and used short
`--c=5` probes, W13 adds:

  - **Linux-side metrics** sampled in the same window as Windows-side.
  - **Sustained traffic** (default 40 pings per direction).
  - **Three-mode coordination** (baseline / force / auto) on all three
    nodes per run.

The two halves do **not** synchronize over the network. The operator
runs the Linux orchestrator on the dev box and the Windows driver on
the Windows machine at roughly the same time per mode. They then
combine the two transcripts.

## Files

  - `_pair.py` — paramiko helpers (host = 216, nat = 36).
  - `w13-linux.py` — Linux side; sets the mode env on 216 + 36, restarts
    `tailscaled-srcsel`, runs sustained TSMP from each Linux peer to
    the other Linux peer and to the Windows peer (v4 + v6 where
    reachable), samples metrics on both peers.
  - `run-w13-windows.ps1` — Windows side; same shape, drives the
    Windows tailscaled and pings the two Linux peers from Windows.
  - `README.md` — this file.

## Prerequisites

### One-time, on the dev box

Install paramiko if not already (W7 / W10 set this up):

```
pip install paramiko
```

Set environment variables (passwords are the IP-allowlist-only ones we
already have in memory; prefer keys if you have them):

```
export SRCSEL_W7_HOST=216.144.236.235
export SRCSEL_W7_PASS=<host pw>
export SRCSEL_W13_NAT_HOST=36.111.166.166
export SRCSEL_W13_NAT_PASS=<nat pw>
```

### One-time, on the Windows clean machine

The Windows side reuses **the W12 pack folder** (e.g. `E:\w12-pack`)
including its already-registered state directory. If you have not run
the W12 driver on this machine yet, run it once first to register the
node:

```
PowerShell -ExecutionPolicy Bypass -File .\run-w12.ps1 -AuthKey "hskey-..."
```

After W12 has populated `state/`, `run-w13-windows.ps1` can be invoked
without an auth key.

## How to run one mode

In two terminals (one on the dev box, one on the Windows clean machine),
roughly at the same time, for a given mode `<m>` ∈ {`baseline`,
`force`, `auto`}:

Dev box:

```
cd multiport/scripts/srcsel-w13
python3 w13-linux.py --mode <m>
```

Windows:

```
cd path-to-srcsel-w13
.\run-w13-windows.ps1 -Mode <m> -PackPath E:\w12-pack
```

Each side prints a self-contained `##### W13 LINUX | mode = <m> #####`
or `##### W13 WINDOWS | mode = <m> #####` block. Capture both into
your phase doc.

Repeat for the other two modes. Total = 6 invocations.

## Tuning knobs

  - `--pings <N>` (Linux) / `-Pings <N>` (Windows): pings per direction
    per mode. Default 40, matching W10 / W11. For an even longer soak
    pass `--pings 200`.
  - `--timeout <S>` (Linux) / `-Timeout <S>` (Windows): per-ping
    timeout. Default 10 s; you should not need to change this.

## What this is not

  - Not a maintained tool. It is a one-time test harness archived in
    the repo so phase docs cite reproducible commands.
  - Not a replacement for the in-tree Go test suite. It exercises the
    real network path; integration tests in `wgengine/magicsock/`
    cover correctness on a single host.
  - Not authenticated. Anyone on the dev box / Windows machine with
    these credentials can execute the same commands. Treat the boxes
    as test infrastructure, not production.

## Phase doc

The validation transcript captured by these scripts is written up in
`docs/tailscale-direct-multisource-udp-phasew13-three-node-coordinated-validation.md`
once the run is complete.
