"""Upload Linux tailscaled-srcsel + tailscale-srcsel to *both* Pair servers via
paramiko sftp.

Looks for binaries in $SRCSEL_W7_BIN_DIR (default: <repo>/.w7-bins). Build them
first with:

    cd <repo>
    GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' \\
        -o <bin-dir>/tailscaled-srcsel ./cmd/tailscaled
    GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' \\
        -o <bin-dir>/tailscale-srcsel  ./cmd/tailscale
"""
from __future__ import annotations

import os
import sys

import _pair

REMOTE_DIR = "/usr/local/bin"
FILES = [("tailscaled-srcsel", 0o755), ("tailscale-srcsel", 0o755)]


def upload(client, bin_dir: str) -> None:
    sftp = client.open_sftp()
    try:
        for name, mode in FILES:
            local = os.path.join(bin_dir, name)
            if not os.path.exists(local):
                sys.stderr.write(f"error: missing {local}\n")
                sys.exit(2)
            remote = f"{REMOTE_DIR}/{name}"
            size = os.path.getsize(local)
            print(f"  upload {name} ({size:,} bytes) -> {remote}")
            sftp.put(local, remote)
            sftp.chmod(remote, mode)
    finally:
        sftp.close()
    _pair.run_named(
        client,
        "verify",
        f"ls -lh {REMOTE_DIR}/tailscale*-srcsel && {REMOTE_DIR}/tailscaled-srcsel --version 2>&1 | head -3",
    )


def main() -> None:
    bin_dir = os.environ.get("SRCSEL_W7_BIN_DIR")
    if not bin_dir:
        bin_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..", ".w7-bins")
    bin_dir = os.path.abspath(bin_dir)
    if not os.path.isdir(bin_dir):
        sys.stderr.write(f"error: bin dir does not exist: {bin_dir}\n")
        sys.exit(2)

    for role, opener in [("host", _pair.open_host), ("client", _pair.open_client)]:
        print(f"\n##### {role} #####")
        c = opener()
        try:
            upload(c, bin_dir)
        finally:
            c.close()


if __name__ == "__main__":
    main()
