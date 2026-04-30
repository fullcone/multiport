"""Shared SSH helpers for the srcsel W13 three-node coordinated validation.

W13 reuses the W12 mesh: Windows client + 216 (public host) + 36 (NAT-pf
host). This module connects to the two Linux peers from the dev box.
The Windows peer is driven by a separate PowerShell script that runs
locally on the Windows machine; the two halves are coordinated by the
operator running them at the same mode at roughly the same time.

Environment variables (mirrors srcsel-w10/_pair.py shape):

    Host (216, public dual-stack, runs headscale):
        SRCSEL_W7_HOST    required (e.g. 216.144.236.235)
        SRCSEL_W7_USER    default: root
        SRCSEL_W7_PASS    required if no key
        SRCSEL_W7_KEY     absolute path to private key (preferred)

    NAT (36, port-forwarded UDP 41641 only):
        SRCSEL_W13_NAT_HOST  required (e.g. 36.111.166.166)
        SRCSEL_W13_NAT_USER  default: root
        SRCSEL_W13_NAT_PASS  required if no key
        SRCSEL_W13_NAT_KEY   absolute path to NAT-side private key

These scripts are a best-effort one-time test harness — see
docs/tailscale-direct-multisource-udp-phasew13-three-node-coordinated.md
for methodology once the validation has been recorded. They are not
maintained tooling.
"""
from __future__ import annotations

import os
import sys

import paramiko


def _need(var: str) -> str:
    val = os.environ.get(var)
    if not val:
        sys.stderr.write(
            f"error: env var {var} is required. See scripts/srcsel-w13/README.md.\n"
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
            f"See scripts/srcsel-w13/README.md.\n"
        )
        sys.exit(2)
    client.connect(**kwargs)
    return client


def open_host(timeout: float = 30.0) -> paramiko.SSHClient:
    """Connect to 216 (public dual-stack, runs headscale)."""
    return _connect("SRCSEL_W7_HOST", "SRCSEL_W7_USER", "SRCSEL_W7_PASS",
                    "SRCSEL_W7_KEY", timeout)


def open_nat(timeout: float = 30.0) -> paramiko.SSHClient:
    """Connect to 36 (NAT-pf, IPv4-only)."""
    return _connect("SRCSEL_W13_NAT_HOST", "SRCSEL_W13_NAT_USER",
                    "SRCSEL_W13_NAT_PASS", "SRCSEL_W13_NAT_KEY", timeout)


def host_address() -> str:
    return _need("SRCSEL_W7_HOST")


def nat_address() -> str:
    return _need("SRCSEL_W13_NAT_HOST")


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
