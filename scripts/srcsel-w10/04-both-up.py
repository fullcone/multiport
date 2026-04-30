"""Bring up tailscaled-srcsel on both Pair servers and register both with
headscale. Mode-driven via SRCSEL_W10_MODE = baseline | force | auto.

First-time registration: also set SRCSEL_W10_FIRST_RUN=1 to invoke
`tailscale up`. Subsequent runs only restart tailscaled with the new env
config; tailnet membership persists in /var/lib/srcsel/."""
from __future__ import annotations

import os
import sys

import _pair

MODE = os.environ.get("SRCSEL_W10_MODE", "baseline").lower()
if MODE == "baseline":
    SRCSEL_ENV = ""
elif MODE == "force":
    SRCSEL_ENV = (
        "TS_EXPERIMENTAL_SRCSEL_ENABLE=true "
        "TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1 "
        "TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux "
    )
elif MODE == "auto":
    SRCSEL_ENV = (
        "TS_EXPERIMENTAL_SRCSEL_ENABLE=true "
        "TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1 "
        "TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true "
    )
else:
    sys.exit(f"bad SRCSEL_W10_MODE: {MODE!r}")

FIRST_RUN = bool(os.environ.get("SRCSEL_W10_FIRST_RUN"))


def cmds(role: str, hostname: str, port: int, login_server: str, auth_key: str):
    base = [
        ("kill prior tailscaled-srcsel",
         "pkill -f tailscaled-srcsel || true; sleep 1; pgrep -af tailscaled-srcsel || echo all-stopped"),
        (f"start tailscaled (mode={MODE}) {role}",
         f"rm -f /var/log/srcsel-tailscaled.log; mkdir -p /var/lib/srcsel; "
         f"{SRCSEL_ENV}"
         f"nohup /usr/local/bin/tailscaled-srcsel "
         f"--tun=userspace-networking "
         f"--socket=/tmp/srcsel.sock "
         f"--statedir=/var/lib/srcsel "
         f"--port={port} "
         f"> /var/log/srcsel-tailscaled.log 2>&1 < /dev/null & "
         f"echo started pid $!; sleep 3; pgrep -af tailscaled-srcsel"),
        ("envknob lines from log",
         "grep -E 'envknob: TS_EXPERIMENTAL_SRCSEL|magicsock: disco key' /var/log/srcsel-tailscaled.log | head -6"),
    ]
    if FIRST_RUN:
        base.append((
            f"tailscale up against headscale ({role})",
            f"/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock up "
            f"--login-server={login_server} "
            f"--auth-key={auth_key} "
            f"--hostname={hostname} "
            f"--accept-routes=false --accept-dns=false 2>&1 | tail -10",
        ))
    base.extend([
        ("status",
         "/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock status 2>&1 | head -10"),
        ("primary + aux UDP sockets",
         "ss -ulnp 2>/dev/null | grep -E 'tailscaled-srcs' | head -10"),
    ])
    return base


def main() -> None:
    auth_key = _pair.auth_key()
    host_addr = _pair.host_address()
    host_login = "http://127.0.0.1:8080"  # local for the host itself
    client_login = f"http://{host_addr}:8080"  # public for the client

    for role, opener, hostname, port, login_server in [
        ("host", _pair.open_host, "srcsel-pair-host", 41641, host_login),
        ("client", _pair.open_client, "srcsel-pair-client", 41642, client_login),
    ]:
        print(f"\n##### {role} on {opener.__name__} ({hostname}) #####")
        c = opener()
        try:
            for label, cmd in cmds(role, hostname, port, login_server, auth_key):
                _pair.run_named(c, label, cmd, timeout=60)
        finally:
            c.close()

    print("\n##### headscale node list (after both joined) #####")
    h = _pair.open_host()
    try:
        _pair.run_named(h, "list", "headscale nodes list 2>&1 | head -5", timeout=30)
    finally:
        h.close()


if __name__ == "__main__":
    main()
