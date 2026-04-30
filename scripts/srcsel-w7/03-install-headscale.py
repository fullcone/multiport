"""Install headscale 0.28.0 on remote via the official .deb release.
Expects the remote to have curl and apt available."""
from __future__ import annotations

import _common

HEADSCALE_VERSION = "0.28.0"
DEB_URL = (
    f"https://github.com/juanfont/headscale/releases/download/"
    f"v{HEADSCALE_VERSION}/headscale_{HEADSCALE_VERSION}_linux_amd64.deb"
)

CMDS = [
    (
        "download deb",
        f"cd /tmp && curl -sSL --retry 3 -o headscale.deb {DEB_URL} && ls -lh /tmp/headscale.deb",
    ),
    (
        "install deb",
        "DEBIAN_FRONTEND=noninteractive apt-get install -y /tmp/headscale.deb 2>&1 | tail -10",
    ),
    ("verify install", "command -v headscale && headscale version"),
    ("default config snippet", "head -40 /etc/headscale/config.yaml 2>&1"),
    ("systemd unit present", "systemctl list-unit-files | grep -i headscale | head -5"),
]


def main() -> None:
    client = _common.open_client(timeout=30)
    try:
        for label, cmd in CMDS:
            _common.run_named(client, label, cmd, timeout=600)
    finally:
        client.close()


if __name__ == "__main__":
    main()
