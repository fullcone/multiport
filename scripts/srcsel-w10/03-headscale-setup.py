"""Install + configure headscale 0.28.0 on the host server, restart it on
0.0.0.0:8080 (so the client can reach it over the public IPv4), open
iptables for tcp/8080, create user, and generate a 24h reusable preauth key.

The auth key is printed to stdout; capture it into SRCSEL_W7_AUTH_KEY for
step 04 (and any subsequent script that touches `tailscale up`)."""
from __future__ import annotations

import _pair


def main() -> None:
    host_addr = _pair.host_address()
    server_url = f"http://{host_addr}:8080"

    cmds = [
        ("download deb",
         "cd /tmp && curl -sSL --retry 3 -o headscale.deb "
         "https://github.com/juanfont/headscale/releases/download/v0.28.0/"
         "headscale_0.28.0_linux_amd64.deb && ls -lh /tmp/headscale.deb"),
        ("install deb",
         "DEBIAN_FRONTEND=noninteractive apt-get install -y /tmp/headscale.deb 2>&1 | tail -5"),
        ("verify install",
         "headscale version | head -3"),
        ("rewrite server_url + listen_addr",
         f"sed -i 's|^server_url:.*|server_url: {server_url}|' /etc/headscale/config.yaml && "
         "sed -i 's|^listen_addr:.*|listen_addr: 0.0.0.0:8080|' /etc/headscale/config.yaml && "
         "grep -E '^(server_url|listen_addr|metrics_listen_addr|grpc_listen_addr):' /etc/headscale/config.yaml"),
        ("enable + restart systemd",
         "systemctl enable headscale 2>&1 | tail -3; systemctl restart headscale && sleep 2 && systemctl is-active headscale"),
        ("verify listening on 0.0.0.0:8080",
         "ss -tlnp | grep -E ':8080' | head -3"),
        ("iptables INPUT for tcp/8080 (idempotent)",
         "iptables -C INPUT -p tcp --dport 8080 -j ACCEPT 2>/dev/null || "
         "iptables -I INPUT -p tcp --dport 8080 -j ACCEPT; "
         "iptables -L INPUT -n | head -8"),
        ("public reachability self-test",
         f"curl -sS -m 5 {server_url}/health"),
        ("create user 'srcsel-pair' (idempotent)",
         "headscale users create srcsel-pair 2>&1 | head -3; headscale users list 2>&1 | head -5"),
        ("generate preauth key for srcsel-pair (24h reusable)",
         "USER_ID=$(headscale users list 2>/dev/null | awk -F'|' '$3 ~ /srcsel-pair/ {gsub(/[^0-9]/,\"\",$1); print $1; exit}'); "
         "if [ -z \"$USER_ID\" ]; then echo 'error: srcsel-pair user ID not found'; exit 1; fi; "
         "echo \"resolved srcsel-pair user ID = $USER_ID\"; "
         "headscale preauthkeys create --user \"$USER_ID\" --reusable --expiration 24h 2>&1 | tail -3"),
    ]

    client = _pair.open_host()
    try:
        for label, cmd in cmds:
            _pair.run_named(client, label, cmd, timeout=600)
        print(
            "\nNOTE: capture the printed hskey-auth-... into SRCSEL_W7_AUTH_KEY "
            "before running 04-both-up.py."
        )
    finally:
        client.close()


if __name__ == "__main__":
    main()
