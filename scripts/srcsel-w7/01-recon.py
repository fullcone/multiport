"""Read-only recon of remote server. Confirms OS, kernel, NIC topology,
listening services, and absence/presence of toolchain (go, tailscale,
headscale)."""
from __future__ import annotations

import _common

CMDS = [
    ("uname", "uname -a"),
    ("os release", "cat /etc/os-release | head -10"),
    ("uptime", "uptime"),
    ("memory", "free -h"),
    ("disk /", "df -h / 2>/dev/null | head -3"),
    ("ip", "ip -br addr | head -20"),
    ("listening", "ss -tunlp 2>/dev/null | head -20 || netstat -tunlp 2>/dev/null | head -20"),
    ("toolchain go", "command -v go && go version || echo 'no-go'"),
    ("toolchain git", "command -v git && git --version || echo 'no-git'"),
    ("toolchain tailscale", "command -v tailscale && tailscale version || echo 'no-tailscale'"),
    ("services", "ps -e o user,comm,args --no-headers 2>/dev/null | grep -v -E '^(root +(kthreadd|ksoftirqd|migration|rcu|systemd|sshd))' | head -25"),
    ("multi-user.target.wants", "ls -la /etc/systemd/system/multi-user.target.wants/ 2>/dev/null | head -20"),
]


def main() -> None:
    client = _common.open_client()
    try:
        for label, cmd in CMDS:
            _common.run_named(client, label, cmd)
    finally:
        client.close()


if __name__ == "__main__":
    main()
