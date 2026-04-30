"""One-time: generate ed25519 key on this Windows host + push pubkey to remote
authorized_keys. Subsequent SSH connections (e.g. the Windows .ps1 helper's
local port-forward) can then run with -i <key> instead of paramiko password
auth.

Reads SRCSEL_W7_KEY for the absolute key path; defaults to
~/.ssh/id_ed25519_srcsel-w7."""
from __future__ import annotations

import os
import subprocess
import sys

import _common


def main() -> None:
    ssh_dir = os.path.expanduser("~/.ssh")
    os.makedirs(ssh_dir, exist_ok=True)
    key = os.environ.get("SRCSEL_W7_KEY") or os.path.join(ssh_dir, "id_ed25519_srcsel-w7")
    pub = key + ".pub"

    if not (os.path.exists(key) and os.path.exists(pub)):
        print(f"generating {key}")
        subprocess.run(
            ["ssh-keygen", "-t", "ed25519", "-f", key, "-N", "", "-C", "srcsel-w7"],
            check=True,
        )
    else:
        print(f"key already exists: {key}")

    with open(pub, "r", encoding="utf-8") as f:
        pubkey = f.read().strip()

    client = _common.open_client(timeout=15)
    try:
        cmd = (
            "mkdir -p /root/.ssh && chmod 700 /root/.ssh && "
            f"grep -qF '{pubkey}' /root/.ssh/authorized_keys 2>/dev/null || "
            f"echo '{pubkey}' >> /root/.ssh/authorized_keys && "
            "chmod 600 /root/.ssh/authorized_keys && wc -l /root/.ssh/authorized_keys"
        )
        _common.run_named(client, "push pubkey + verify", cmd, timeout=30)
    finally:
        client.close()

    user = os.environ.get("SRCSEL_W7_USER", "root")
    host = os.environ["SRCSEL_W7_HOST"]
    print("\nverifying key auth...")
    res = subprocess.run(
        [
            "ssh",
            "-i",
            key,
            "-o",
            "StrictHostKeyChecking=no",
            "-o",
            "PasswordAuthentication=no",
            "-o",
            "BatchMode=yes",
            f"{user}@{host}",
            "echo key-auth-ok",
        ],
        capture_output=True,
        text=True,
        timeout=20,
    )
    print(f"returncode={res.returncode}")
    if res.stdout.strip():
        print(f"stdout: {res.stdout.strip()}")
    if res.stderr.strip():
        print(f"stderr: {res.stderr.strip()}")
    if res.returncode != 0:
        sys.exit(res.returncode)


if __name__ == "__main__":
    main()
