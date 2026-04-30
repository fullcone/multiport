"""TSMP ping in both directions over IPv4 and IPv6 + capture
magicsock_srcsel_* metrics from both ends. Re-run after each mode switch
(baseline / force / auto). Set LABEL env to tag the output.

Tailnet IPv4 / IPv6 addresses are discovered at run time from each side's
own `tailscale status --json`, matched by the explicit hostnames the W10
helpers register with headscale (`srcsel-pair-host` and
`srcsel-pair-client`). This stays correct on tailnets that already have
unrelated nodes from earlier runs."""
from __future__ import annotations

import ipaddress
import json
import os
import sys

import _pair

LABEL = os.environ.get("LABEL", "run")
HOST_HOSTNAME = os.environ.get("SRCSEL_W10_HOST_HOSTNAME", "srcsel-pair-host")
CLIENT_HOSTNAME = os.environ.get("SRCSEL_W10_CLIENT_HOSTNAME", "srcsel-pair-client")


def ts(c, *args):
    cmd = "/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock " + " ".join(args)
    _, stdout, stderr = c.exec_command(cmd, timeout=60)
    return stdout.read().decode("utf-8", errors="replace").rstrip(), stderr.read().decode("utf-8", errors="replace").rstrip()


def discover_ips(c) -> tuple[list[str], dict[str, list[str]]]:
    """Return (self_ips, peer_ips_by_hostname) from `tailscale status --json`.

    Each list contains all of the node's TailscaleIPs (mixed v4 + v6)."""
    out, err = ts(c, "status --json")
    if not out:
        sys.exit(f"could not read tailscale status JSON: {err}")
    data = json.loads(out)
    self_ips = list(data.get("Self", {}).get("TailscaleIPs") or [])
    peers: dict[str, list[str]] = {}
    for peer in (data.get("Peer") or {}).values():
        host = peer.get("HostName") or peer.get("DNSName") or ""
        ips = list(peer.get("TailscaleIPs") or [])
        if host and ips:
            peers[host] = ips
    return self_ips, peers


def split_v4_v6(ips: list[str]) -> tuple[list[str], list[str]]:
    v4: list[str] = []
    v6: list[str] = []
    for ip in ips:
        try:
            addr = ipaddress.ip_address(ip)
        except ValueError:
            continue
        (v4 if addr.version == 4 else v6).append(ip)
    return v4, v6


def main() -> None:
    h = _pair.open_host()
    cl = _pair.open_client()
    try:
        _, host_peers = discover_ips(h)
        _, client_peers = discover_ips(cl)

        # Match by exact hostname so reused tailnets with unrelated peers do
        # not steer the test at the wrong node.
        def _peer_by_name(peers: dict[str, list[str]], wanted: str, view_label: str) -> list[str]:
            ips = peers.get(wanted)
            if not ips:
                sys.exit(
                    f"{view_label} sees no peer named {wanted!r}; "
                    f"available peers: {sorted(peers.keys())}"
                )
            return ips

        client_ips_from_host = _peer_by_name(host_peers, CLIENT_HOSTNAME, "host")
        host_ips_from_client = _peer_by_name(client_peers, HOST_HOSTNAME, "client")
        host_v4, host_v6 = split_v4_v6(host_ips_from_client)
        client_v4, client_v6 = split_v4_v6(client_ips_from_host)
        print(f"discovered host  v4={host_v4} v6={host_v6}")
        print(f"discovered client v4={client_v4} v6={client_v6}")

        cases = []
        if host_v4:
            cases.append((f"{LABEL}: TSMP client -> host (IPv4)", cl, host_v4[0], "5"))
        if client_v4:
            cases.append((f"{LABEL}: TSMP host -> client (IPv4)", h, client_v4[0], "5"))
        if host_v6:
            cases.append((f"{LABEL}: TSMP client -> host (IPv6)", cl, host_v6[0], "3"))
        if client_v6:
            cases.append((f"{LABEL}: TSMP host -> client (IPv6)", h, client_v6[0], "3"))

        for label, c, dst, count in cases:
            print(f"\n##### {label} #####")
            out, err = ts(c, f"ping --tsmp --c={count} --timeout=10s {dst}")
            print(out)
            if err:
                print("[stderr]", err)

        for label, c in [(f"{LABEL}: srcsel metrics on host", h),
                         (f"{LABEL}: srcsel metrics on client", cl)]:
            print(f"\n##### {label} #####")
            out, _ = ts(c, "debug metrics")
            for line in out.splitlines():
                if line.startswith("magicsock_srcsel"):
                    print(line)

        print(f"\n##### {LABEL}: status on host (look for direct via) #####")
        out, _ = ts(h, "status --self=false --peers=true 2>&1 | head -8")
        print(out)
    finally:
        h.close()
        cl.close()


if __name__ == "__main__":
    main()
