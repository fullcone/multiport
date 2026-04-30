"""Read-only recon of both Pair servers — OS, NIC, public IPv4/IPv6 reachability,
listening ports, toolchain. Produces side-by-side output."""
from __future__ import annotations

import _pair

CMDS = [
    ("uname", "uname -a"),
    ("os release", "head -3 /etc/os-release"),
    ("cpu/mem", "nproc; free -h | head -2"),
    ("ip -br addr (note v4 + v6 prefixes)", "ip -br addr"),
    ("ipv6 default route", "ip -6 route show default 2>/dev/null"),
    ("public ipv4 self-test", "curl -4 -sS -m 5 https://api.ipify.org 2>/dev/null && echo"),
    ("public ipv6 self-test", "curl -6 -sS -m 5 https://api6.ipify.org 2>/dev/null && echo"),
    ("listening", "ss -tunlp 2>/dev/null | head -10"),
    ("toolchain", "command -v go; command -v tailscale; command -v headscale"),
    ("ufw / iptables", "ufw status 2>/dev/null | head -5; echo ---; iptables -L INPUT -n 2>/dev/null | head -5"),
]


def main() -> None:
    for role, opener in [("host (headscale)", _pair.open_host), ("client", _pair.open_client)]:
        print(f"\n##### {role} #####")
        client = opener()
        try:
            for label, cmd in CMDS:
                _pair.run_named(client, label, cmd, timeout=15)
        finally:
            client.close()


if __name__ == "__main__":
    main()
