"""W13 Linux side: drive 216 (host) + 36 (nat) for one srcsel mode.

Runs on the dev box. Uses paramiko via _pair.py. For one invocation it:

  1. Stops any running tailscaled-srcsel on both peers.
  2. Sets the mode env knobs (TS_EXPERIMENTAL_SRCSEL_*) for the chosen
     mode and re-launches tailscaled-srcsel under nohup with the
     persisted state dir.
  3. Waits 8 s for the data plane to warm up.
  4. Discovers the three tailnet peers from the 216 side
     (`tailscale status --json`).
  5. Issues sustained TSMP pings (default 40 per direction) from each
     of host (216) and nat (36) to the other two peers, on IPv4 and
     IPv6 where reachable.
  6. Samples magicsock_srcsel_* metrics on both peers.
  7. Prints a labelled transcript suitable for combining with the
     Windows-side `run-w13-windows.ps1` transcript.

Pair this with `run-w13-windows.ps1 -Mode <same>` started at roughly
the same time on the clean Windows machine. The two halves do not
synchronize over the network; the operator chooses the mode for both.

Usage:
    python3 w13-linux.py --mode baseline
    python3 w13-linux.py --mode force
    python3 w13-linux.py --mode auto

Environment variables: see _pair.py docstring.
"""
from __future__ import annotations

import argparse
import json
import sys
import time

import _pair


WINDOWS_HOSTNAME_PREFIX = "srcsel-w12-clean-"  # what run-w13-windows.ps1 sets
HOST_HOSTNAME = "srcsel-pair2-host"
NAT_HOSTNAME = "srcsel-w12-nat-server"

DEFAULT_PINGS = 40
DEFAULT_TIMEOUT_S = 10
WARMUP_S = 8
PEER_DISCOVERY_DEADLINE_S = 60


def env_for_mode(mode: str) -> str:
    if mode == "baseline":
        return ""
    if mode == "force":
        return ("TS_EXPERIMENTAL_SRCSEL_ENABLE=true "
                "TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1 "
                "TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux ")
    if mode == "auto":
        return ("TS_EXPERIMENTAL_SRCSEL_ENABLE=true "
                "TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1 "
                "TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true ")
    sys.exit(f"unknown --mode {mode!r}; expected baseline / force / auto")


def restart_remote(client, env: str, mode: str, role: str) -> None:
    cmd = (
        f"pkill -f tailscaled-srcsel || true; sleep 1; "
        f"rm -f /var/log/srcsel-tailscaled.log; "
        f"mkdir -p /var/lib/srcsel; "
        f"{env}"
        f"nohup /usr/local/bin/tailscaled-srcsel "
        f"--tun=userspace-networking --socket=/tmp/srcsel.sock "
        f"--statedir=/var/lib/srcsel --port=41641 "
        f"> /var/log/srcsel-tailscaled.log 2>&1 < /dev/null & "
        f"echo started pid $!; sleep 3; pgrep -af tailscaled-srcsel"
    )
    _pair.run_named(client, f"restart {role} (mode={mode})", cmd)


def ts_status_json(client) -> dict:
    out, _ = _pair.run_named(
        client, "status --json (host)",
        "/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock status --json",
        timeout=20)
    try:
        return json.loads(out) if out else {}
    except json.JSONDecodeError as e:
        sys.exit(f"failed to parse tailscale status JSON: {e}\n{out[:400]}...")


def find_peer_addrs(status: dict, hostname_match) -> tuple[str | None, str | None]:
    """Return (v4, v6) for the first peer whose HostName matches.

    hostname_match may be a string (exact) or a callable taking str → bool.
    """
    peers = (status.get("Peer") or {}).values()
    for peer in peers:
        host = peer.get("HostName") or ""
        ok = hostname_match(host) if callable(hostname_match) else (host == hostname_match)
        if not ok:
            continue
        v4 = v6 = None
        for ip in peer.get("TailscaleIPs") or []:
            if ":" in ip and v6 is None:
                v6 = ip
            elif "." in ip and v4 is None:
                v4 = ip
        return v4, v6
    return None, None


def tsmp(client, role: str, dst: str, label: str, count: int, timeout: int) -> None:
    cmd = (f"/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock "
           f"ping --tsmp --c={count} --timeout={timeout}s {dst}")
    _pair.run_named(client, f"TSMP {role} {label} (-> {dst})", cmd,
                    timeout=count * (timeout + 2))


def metrics(client, role: str) -> None:
    cmd = ("/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock "
           "debug metrics 2>&1 | grep -E '^magicsock_srcsel'")
    _pair.run_named(client, f"metrics {role}", cmd)


def main() -> None:
    p = argparse.ArgumentParser()
    p.add_argument("--mode", required=True, choices=("baseline", "force", "auto"))
    p.add_argument("--pings", type=int, default=DEFAULT_PINGS,
                   help=f"TSMP pings per direction (default {DEFAULT_PINGS})")
    p.add_argument("--timeout", type=int, default=DEFAULT_TIMEOUT_S,
                   help=f"per-ping timeout in seconds (default {DEFAULT_TIMEOUT_S})")
    p.add_argument("--allow-missing-windows", action="store_true",
                   help="proceed even if the Windows peer is not seen "
                        "within the discovery deadline (default: fail). "
                        "Use only when intentionally running Linux side "
                        "alone, e.g. for re-collecting metrics after a "
                        "coordinated mode change.")
    args = p.parse_args()

    env = env_for_mode(args.mode)

    print(f"\n##### W13 LINUX | mode = {args.mode} #####")
    print(f"  host   = {_pair.host_address()}")
    print(f"  nat    = {_pair.nat_address()}")

    h = _pair.open_host()
    n = _pair.open_nat()
    try:
        restart_remote(h, env, args.mode, role="host(216)")
        restart_remote(n, env, args.mode, role="nat(36)")

        print(f"\n--- warmup {WARMUP_S}s")
        time.sleep(WARMUP_S)

        # Wait up to PEER_DISCOVERY_DEADLINE_S for all three peers
        # (including the Windows side started by the operator's
        # parallel run-w13-windows.ps1) to appear in 216's status.
        # Without this block, an early Linux-side run before the
        # Windows side has rejoined would silently skip the four
        # Linux→Win directions and still produce a transcript that
        # looks like a complete coordinated run.
        deadline = time.monotonic() + PEER_DISCOVERY_DEADLINE_S
        host_v4 = host_v6 = nat_v4 = nat_v6 = win_v4 = win_v6 = None
        while time.monotonic() < deadline:
            status = ts_status_json(h)
            host_v4, host_v6 = find_peer_addrs(status, HOST_HOSTNAME)
            if host_v4 is None and host_v6 is None:
                # Self is host; pull our own TailscaleIPs from status.Self.
                self_node = status.get("Self") or {}
                for ip in self_node.get("TailscaleIPs") or []:
                    if ":" in ip:
                        host_v6 = host_v6 or ip
                    else:
                        host_v4 = host_v4 or ip
            nat_v4, nat_v6 = find_peer_addrs(status, NAT_HOSTNAME)
            win_v4, win_v6 = find_peer_addrs(
                status, lambda hn: hn.startswith(WINDOWS_HOSTNAME_PREFIX))
            if host_v4 and nat_v4 and (win_v4 or args.allow_missing_windows):
                break
            time.sleep(2)

        print(f"\n--- discovered tailnet addrs")
        print(f"  host: v4={host_v4}  v6={host_v6}")
        print(f"  nat : v4={nat_v4}   v6={nat_v6}")
        print(f"  win : v4={win_v4}   v6={win_v6}")

        if not host_v4:
            sys.exit(f"required peer {HOST_HOSTNAME!r} (host 216) was not "
                     f"discovered in {PEER_DISCOVERY_DEADLINE_S}s — abort")
        if not nat_v4:
            sys.exit(f"required peer {NAT_HOSTNAME!r} (nat 36) was not "
                     f"discovered in {PEER_DISCOVERY_DEADLINE_S}s — abort")
        if not win_v4:
            if not args.allow_missing_windows:
                sys.exit(
                    f"required peer (Windows, hostname starting with "
                    f"{WINDOWS_HOSTNAME_PREFIX!r}) was not discovered in "
                    f"{PEER_DISCOVERY_DEADLINE_S}s. Make sure "
                    f"`run-w13-windows.ps1 -Mode {args.mode}` is running "
                    f"on the Windows clean machine before starting the "
                    f"Linux orchestrator. Pass --allow-missing-windows "
                    f"to deliberately skip Linux→Win directions.")
            print("[warn] proceeding without Windows peer per "
                  "--allow-missing-windows; Linux→Win directions will "
                  "be skipped.")

        # Linux-side TSMP. Eight directions; v6 ones are skipped if a
        # peer has no IPv6 (nat = 36 is IPv4-only; the Windows clean
        # machine in the W12 setup typically has no IPv6 either).
        for label, src_client, dst in [
            ("host -> nat (v4)", h, nat_v4),
            ("host -> nat (v6)", h, nat_v6),
            ("host -> win  (v4)", h, win_v4),
            ("host -> win  (v6)", h, win_v6),
            ("nat  -> host (v4)", n, host_v4),
            ("nat  -> host (v6)", n, host_v6),
            ("nat  -> win  (v4)", n, win_v4),
            ("nat  -> win  (v6)", n, win_v6),
        ]:
            if not dst:
                print(f"\n--- skip {label} (no peer addr)")
                continue
            tsmp(src_client, "linux", dst, label, args.pings, args.timeout)

        metrics(h, "host(216)")
        metrics(n, "nat(36)")
    finally:
        h.close()
        n.close()


if __name__ == "__main__":
    main()
