"""Start fullcone/multiport tailscaled-srcsel on remote in userspace mode and
register against the local headscale.

Use the SRCSEL_W7_MODE env var to pick between baseline / forced-aux / auto:

    SRCSEL_W7_MODE=baseline   # no srcsel env vars
    SRCSEL_W7_MODE=force      # TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux
    SRCSEL_W7_MODE=auto       # TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true
"""
from __future__ import annotations

import os
import sys

import _common

MODE = os.environ.get("SRCSEL_W7_MODE", "baseline").lower()
if MODE not in {"baseline", "force", "auto"}:
    sys.stderr.write(f"error: SRCSEL_W7_MODE must be baseline|force|auto (got {MODE!r})\n")
    sys.exit(2)

if MODE == "baseline":
    SRCSEL_ENV = ""
elif MODE == "force":
    SRCSEL_ENV = (
        "TS_EXPERIMENTAL_SRCSEL_ENABLE=true "
        "TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1 "
        "TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux "
    )
else:  # auto
    SRCSEL_ENV = (
        "TS_EXPERIMENTAL_SRCSEL_ENABLE=true "
        "TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1 "
        "TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true "
    )


def main() -> None:
    auth_key = _common.auth_key()
    is_first_run = MODE == "baseline" and "_FIRST_RUN" in os.environ

    cmds = [
        ("kill any prior tailscaled-srcsel",
         "pkill -f tailscaled-srcsel || true; sleep 1; pgrep -af tailscaled-srcsel || echo all-stopped"),
        (f"restart tailscaled (mode={MODE})",
         f"rm -f /var/log/srcsel-tailscaled.log; mkdir -p /var/lib/srcsel; "
         f"{SRCSEL_ENV}"
         f"nohup /usr/local/bin/tailscaled-srcsel "
         f"--tun=userspace-networking "
         f"--socket=/tmp/srcsel.sock "
         f"--statedir=/var/lib/srcsel "
         f"--port=41641 "
         f"> /var/log/srcsel-tailscaled.log 2>&1 < /dev/null & "
         f"echo started pid $!; sleep 3; pgrep -af tailscaled-srcsel"),
        ("first 25 log lines (look for envknob lines)",
         "head -25 /var/log/srcsel-tailscaled.log 2>/dev/null"),
    ]

    if is_first_run:
        cmds.append((
            "tailscale up against local headscale",
            f"/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock up "
            f"--login-server=http://127.0.0.1:8080 "
            f"--auth-key={auth_key} "
            f"--hostname=srcsel-server "
            f"--accept-routes=false --accept-dns=false 2>&1 | tail -20",
        ))

    cmds.extend([
        ("status",
         "/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock status 2>&1 | head -20"),
        ("aux + primary UDP sockets bound",
         "ss -ulnp 2>/dev/null | grep -E 'tailscaled-srcs' | head -10"),
        ("headscale node list",
         "headscale nodes list 2>&1 | head -10"),
    ])

    client = _common.open_client(timeout=30)
    try:
        for label, cmd in cmds:
            _common.run_named(client, label, cmd, timeout=60)
    finally:
        client.close()


if __name__ == "__main__":
    main()
