"""Drive 20 rounds of sustained TSMP traffic from the host server, then
capture host-side srcsel metrics. Used in W10 to exercise Phase 20's
primary-baseline gate under low-RTT direct paths.

The peer's tailnet IPv4 is discovered at run time from
`tailscale status --json` so the script targets the actual peer
regardless of headscale's IP allocation order."""
from __future__ import annotations

import ipaddress
import json
import sys
import time

import _pair


def discover_peer_ipv4(c) -> str:
    cmd = "/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock status --json"
    _, stdout, stderr = c.exec_command(cmd, timeout=30)
    out = stdout.read().decode("utf-8", errors="replace").rstrip()
    err = stderr.read().decode("utf-8", errors="replace").rstrip()
    if not out:
        sys.exit(f"could not read tailscale status JSON: {err}")
    data = json.loads(out)
    self_ips = set(data.get("Self", {}).get("TailscaleIPs") or [])
    for peer in (data.get("Peer") or {}).values():
        for ip in peer.get("TailscaleIPs") or []:
            if ip in self_ips:
                continue
            try:
                if ipaddress.ip_address(ip).version == 4:
                    return ip
            except ValueError:
                pass
    sys.exit("no peer IPv4 address found in tailscale status — is the other side joined?")


def main() -> None:
    h = _pair.open_host()
    try:
        peer_v4 = discover_peer_ipv4(h)
        print(f"sustained pings from host -> {peer_v4} (peer IPv4 from tailscale status)")
        for i in range(20):
            _, stdout, _ = h.exec_command(
                f"/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock "
                f"ping --tsmp --c=2 --timeout=8s {peer_v4} 2>&1",
                timeout=30)
            line = stdout.read().decode("utf-8", errors="replace").splitlines()
            print(f"round {i+1:>2}: {line[0] if line else '(no output)'}")
            time.sleep(0.3)

        print("\n##### host srcsel metrics after 20 rounds #####")
        _, stdout, _ = h.exec_command(
            "/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock debug metrics 2>&1 | grep ^magicsock_srcsel",
            timeout=30)
        print(stdout.read().decode("utf-8", errors="replace").rstrip())
    finally:
        h.close()


if __name__ == "__main__":
    main()
