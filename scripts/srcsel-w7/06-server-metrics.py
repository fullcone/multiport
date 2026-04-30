"""Capture server-side magicsock_srcsel_* metrics + recent srcsel-related log
lines + tailscale status. Read-only."""
from __future__ import annotations

import _common

CMDS = [
    (
        "server srcsel metrics",
        "/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock debug metrics 2>&1 | grep -E '^magicsock_srcsel'",
    ),
    (
        "server status",
        "/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock status 2>&1",
    ),
    (
        "server tailscaled log: srcsel + magicsock data path hints (last 20 matching)",
        "tail -300 /var/log/srcsel-tailscaled.log | grep -E 'srcsel:|lazyEndpoint|aux_wireguard|disco: node' | tail -20",
    ),
]


def main() -> None:
    client = _common.open_client(timeout=30)
    try:
        for label, cmd in CMDS:
            _common.run_named(client, label, cmd, timeout=30)
    finally:
        client.close()


if __name__ == "__main__":
    main()
