"""Shared SSH/SFTP helpers for the srcsel W7 bilateral validation scripts.

Reads connection config from environment variables:
    SRCSEL_W7_HOST       remote host or IP (required)
    SRCSEL_W7_USER       remote SSH user (default: root)
    SRCSEL_W7_PASS       remote SSH password (required if no key)
    SRCSEL_W7_KEY        absolute path to private key (optional, takes precedence
                         over password)
    SRCSEL_W7_AUTH_KEY   headscale preauth key, e.g. hskey-auth-... (required for
                         w7-server-up.py and the Windows .ps1 helpers)

These scripts are best-effort, one-time test harness — see
docs/tailscale-direct-multisource-udp-phasew7-bilateral-real-network-validation.md
for the methodology and findings. They are not maintained tooling.
"""
from __future__ import annotations

import os
import sys
from typing import Optional

import paramiko


def _need(var: str) -> str:
    val = os.environ.get(var)
    if not val:
        sys.stderr.write(
            f"error: env var {var} is required. See scripts/srcsel-w7/README.md.\n"
        )
        sys.exit(2)
    return val


def open_client(timeout: float = 30.0) -> paramiko.SSHClient:
    """Connect to the remote and return a paramiko SSHClient.

    Caller owns the lifecycle and must call ``client.close()``.
    """
    host = _need("SRCSEL_W7_HOST")
    user = os.environ.get("SRCSEL_W7_USER", "root")
    key_path = os.environ.get("SRCSEL_W7_KEY")
    password = os.environ.get("SRCSEL_W7_PASS")

    client = paramiko.SSHClient()
    client.set_missing_host_key_policy(paramiko.AutoAddPolicy())

    kwargs = {
        "hostname": host,
        "username": user,
        "timeout": timeout,
        "look_for_keys": False,
        "allow_agent": False,
    }
    if key_path:
        kwargs["key_filename"] = key_path
    elif password:
        kwargs["password"] = password
    else:
        sys.stderr.write(
            "error: neither SRCSEL_W7_KEY nor SRCSEL_W7_PASS is set. See scripts/srcsel-w7/README.md.\n"
        )
        sys.exit(2)
    client.connect(**kwargs)
    return client


def auth_key() -> str:
    return _need("SRCSEL_W7_AUTH_KEY")


def run_named(client: paramiko.SSHClient, label: str, cmd: str, timeout: float = 60.0) -> tuple[str, str]:
    """Execute ``cmd`` on the remote, print labelled stdout/stderr, return them."""
    print(f"\n=== {label}")
    stdin, stdout, stderr = client.exec_command(cmd, timeout=timeout, get_pty=False)
    out = stdout.read().decode("utf-8", errors="replace").rstrip()
    err = stderr.read().decode("utf-8", errors="replace").rstrip()
    if out:
        print(out)
    if err:
        print(f"[stderr] {err}", file=sys.stderr)
    return out, err
