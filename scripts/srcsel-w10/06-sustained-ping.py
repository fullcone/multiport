"""Drive 20 rounds of sustained TSMP traffic from the host server, then
capture host-side srcsel metrics. Used in W10 to exercise Phase 20's
primary-baseline gate under low-RTT direct paths."""
from __future__ import annotations

import time

import _pair


def main() -> None:
    h = _pair.open_host()
    try:
        for i in range(20):
            _, stdout, _ = h.exec_command(
                "/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock "
                "ping --tsmp --c=2 --timeout=8s 100.64.0.2 2>&1",
                timeout=30)
            line = stdout.read().decode("utf-8", errors="replace").splitlines()
            print(f"round {i+1:>2}: {line[0] if line else '(no output)'}")
            time.sleep(0.3)

        print("\n##### host srcsel metrics after 20 rounds #####")
        _, stdout, _ = h.exec_command(
            "/usr/local/bin/tailscale-srcsel --socket=/tmp/srcsel.sock debug metrics 2>&1 | grep ^magicsock_srcsel",
            timeout=30)
        print(stdout.read().decode("utf-8", errors="replace").rstrip())
    finally:
        h.close()


if __name__ == "__main__":
    main()
