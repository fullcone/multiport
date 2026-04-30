"""TSMP ping in both directions over IPv4 and IPv6 + capture
magicsock_srcsel_* metrics from both ends. Re-run after each mode switch
(baseline / force / auto). Set LABEL env to tag the output."""
from __future__ import annotations

import os

import _pair

HOST_TS_IP = "100.64.0.1"
CLIENT_TS_IP = "100.64.0.2"
HOST_TS_IP6 = "fd7a:115c:a1e0::1"
CLIENT_TS_IP6 = "fd7a:115c:a1e0::2"
LABEL = os.environ.get("LABEL", "run")


def ts(c, *args):
    cmd = "/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock " + " ".join(args)
    _, stdout, stderr = c.exec_command(cmd, timeout=60)
    return stdout.read().decode("utf-8", errors="replace").rstrip(), stderr.read().decode("utf-8", errors="replace").rstrip()


def main() -> None:
    h = _pair.open_host()
    cl = _pair.open_client()
    try:
        for label, c, dst in [
            (f"{LABEL}: TSMP client -> host (IPv4)", cl, HOST_TS_IP),
            (f"{LABEL}: TSMP host -> client (IPv4)", h, CLIENT_TS_IP),
            (f"{LABEL}: TSMP client -> host (IPv6)", cl, HOST_TS_IP6),
            (f"{LABEL}: TSMP host -> client (IPv6)", h, CLIENT_TS_IP6),
        ]:
            print(f"\n##### {label} #####")
            count = "5" if dst.startswith("100.") else "3"
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
