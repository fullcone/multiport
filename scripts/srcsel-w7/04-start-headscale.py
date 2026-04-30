"""Start headscale, create user 'srcsel', generate a 24h reusable preauth key.

The generated auth key is printed to stdout. Capture it and pass it to the
later scripts via the SRCSEL_W7_AUTH_KEY env var.
"""
from __future__ import annotations

import _common

CMDS = [
    (
        "enable + start systemd service",
        "systemctl enable --now headscale && sleep 2 && systemctl is-active headscale",
    ),
    ("verify listening", "ss -tlnp | grep -E '8080|9090|50443' | head -5"),
    ("headscale CLI status", "headscale users list 2>&1 || true"),
    ("create user 'srcsel'", "headscale users create srcsel 2>&1 | head -5"),
    ("list users", "headscale users list 2>&1 | head -5"),
    (
        "generate preauth key (24h reusable)",
        "headscale preauthkeys create --user 1 --reusable --expiration 24h 2>&1 | tail -3",
    ),
]


def main() -> None:
    client = _common.open_client(timeout=30)
    try:
        for label, cmd in CMDS:
            _common.run_named(client, label, cmd, timeout=60)
        print(
            "\nNOTE: set SRCSEL_W7_AUTH_KEY=<the hskey-auth-... value above> for"
            " the next scripts."
        )
    finally:
        client.close()


if __name__ == "__main__":
    main()
