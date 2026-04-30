"""Shared SSH/SFTP helpers for the srcsel W10 both-public Linux-pair scripts.

Reads connection config for *two* remote hosts from environment variables:

    Host (the server that runs headscale + tailscaled):
        SRCSEL_W7_HOST           required
        SRCSEL_W7_USER           default: root
        SRCSEL_W7_PASS           required if no key
        SRCSEL_W7_KEY            absolute path to private key (preferred)
        SRCSEL_W7_AUTH_KEY       headscale preauth key from step 03

    Client (the second server that joins the tailnet):
        SRCSEL_W10_CLIENT_HOST   required
        SRCSEL_W10_CLIENT_USER   default: root
        SRCSEL_W10_CLIENT_PASS   required if no client key
        SRCSEL_W10_CLIENT_KEY    absolute path to client private key

These scripts are best-effort, one-time test harness — see
docs/tailscale-direct-multisource-udp-phasew10-linux-public-pair-validation.md
for the methodology and findings. They are not maintained tooling.
"""
from __future__ import annotations

import os
import sys

import paramiko


def _need(var: str) -> str:
    val = os.environ.get(var)
    if not val:
        sys.stderr.write(
            f"error: env var {var} is required. See scripts/srcsel-w10/README.md.\n"
        )
        sys.exit(2)
    return val


def _connect(host_var: str, user_var: str, pass_var: str, key_var: str,
             timeout: float) -> paramiko.SSHClient:
    host = _need(host_var)
    user = os.environ.get(user_var, "root")
    key_path = os.environ.get(key_var)
    password = os.environ.get(pass_var)

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
            f"error: neither {key_var} nor {pass_var} is set. "
            f"See scripts/srcsel-w10/README.md.\n"
        )
        sys.exit(2)
    client.connect(**kwargs)
    return client


def open_host(timeout: float = 30.0) -> paramiko.SSHClient:
    """Connect to the headscale-running server (SRCSEL_W7_*)."""
    return _connect("SRCSEL_W7_HOST", "SRCSEL_W7_USER", "SRCSEL_W7_PASS",
                    "SRCSEL_W7_KEY", timeout)


def open_client(timeout: float = 30.0) -> paramiko.SSHClient:
    """Connect to the joining server (SRCSEL_W10_CLIENT_*)."""
    return _connect("SRCSEL_W10_CLIENT_HOST", "SRCSEL_W10_CLIENT_USER",
                    "SRCSEL_W10_CLIENT_PASS", "SRCSEL_W10_CLIENT_KEY", timeout)


def auth_key() -> str:
    return _need("SRCSEL_W7_AUTH_KEY")


def host_address() -> str:
    return _need("SRCSEL_W7_HOST")


def run_named(client: paramiko.SSHClient, label: str, cmd: str,
              timeout: float = 60.0) -> tuple[str, str]:
    """Execute cmd on the remote, print labelled stdout/stderr, return them."""
    print(f"\n--- {label}")
    stdin, stdout, stderr = client.exec_command(cmd, timeout=timeout, get_pty=False)
    out = stdout.read().decode("utf-8", errors="replace").rstrip()
    err = stderr.read().decode("utf-8", errors="replace").rstrip()
    if out:
        print(out)
    if err:
        print(f"[stderr] {err}", file=sys.stderr)
    return out, err
