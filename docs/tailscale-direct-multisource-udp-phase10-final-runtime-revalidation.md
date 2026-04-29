# Tailscale Direct Multisource UDP Phase 10 Final Runtime Revalidation

Date: 2026-04-29

This document records the post-Phase 9 Linux dual-node runtime revalidation for
the current PR head of `fullcone/multiport`.

The goal is to prove the behavior that matters before moving beyond the
implementation phases:

- forced auxiliary data send emits real WireGuard UDP packets from the
  auxiliary source socket
- automatic auxiliary data send emits real WireGuard UDP packets from the
  selected auxiliary source socket
- both behaviors work for IPv4 and IPv6
- injected auxiliary send failure retries the primary socket
- successful auxiliary sends and fallback sends do not update
  `Conn.lastErrRebind`
- primary rebind logic is not used as a side effect of source selection

## PR Review Gate

Current PR state before this runtime revalidation:

- PR: `https://github.com/fullcone/multiport/pull/1`
- PR head before this document: `30766aa4c8116ee6ffd89011f57cae520e69414c`
- PR was open and mergeable.
- No current blocking review thread was present.
- The only unresolved thread was outdated:
  `PRRT_kwDOSPBZuM5-U8eD`.
- That outdated thread was the Phase 4B P2 `sourcePathSocket.rxMeta`
  synchronization finding, already fixed by commit
  `5c6738d84cca8f09d896e82c375a181c97158b8a`.
- Phase 9 implementation review response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343111677`.
- Phase 9 doc-only review response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343132051`.
- Both Phase 9 Codex responses reported no major issues.

## Runtime Tests

`wgengine/magicsock/sourcepath_linux_test.go` contains the final dual-node
runtime checks:

- `TestSourcePathForcedAuxDualNodeRuntime`
- `TestSourcePathAutomaticAuxDualNodeRuntime`

The forced runtime test sets:

```text
TS_EXPERIMENTAL_SRCSEL_ENABLE=true
TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1
TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux
TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=
```

It runs both IPv4 and IPv6 subtests. For each address family it:

- builds a dual-node magicsock fixture
- waits for an auxiliary source socket
- finds the current direct peer endpoint
- stores `time.Unix(123, 0)` into `lastErrRebind`
- sends a WireGuard packet and verifies the packet was written from the
  auxiliary local address to the peer endpoint
- injects `EPERM` for the auxiliary source address
- sends again and verifies a primary-socket retry
- verifies `lastErrRebind` stayed equal to the sentinel value

The automatic runtime test sets:

```text
TS_EXPERIMENTAL_SRCSEL_ENABLE=true
TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1
TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=
TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE=true
```

It runs both IPv4 and IPv6 subtests. For each address family it:

- builds a dual-node magicsock fixture
- waits for an auxiliary source socket
- seeds a source-path candidate for the direct peer endpoint
- verifies automatic source selection picks the seeded candidate
- stores `time.Unix(456, 0)` into `lastErrRebind`
- sends a WireGuard packet and verifies the packet was written from the
  selected auxiliary local address to the peer endpoint
- injects `EPERM` for the auxiliary source address
- sends again and verifies a primary-socket retry
- verifies `lastErrRebind` stayed equal to the sentinel value

## Validation Command

Full WSL runtime command:

```powershell
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/zerotier-client/multiport -- bash -lc 'go test ./wgengine/magicsock -run "TestSourcePath(ForcedAuxDualNodeRuntime|AutomaticAuxDualNodeRuntime)" -count=1 -v'
```

Result:

```text
PASS
ok  	tailscale.com/wgengine/magicsock	0.277s
```

The WSL host printed a localhost/NAT warning after the test output, but the Go
test command exited successfully.

Clean evidence extraction command:

```powershell
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/zerotier-client/multiport -- bash -lc 'go test ./wgengine/magicsock -run "TestSourcePath(ForcedAuxDualNodeRuntime|AutomaticAuxDualNodeRuntime)" -count=1 -v 2>&1 | grep -E "runtime path|retrying primary|--- PASS: TestSourcePath|^PASS$|^ok"'
```

Extracted evidence:

```text
    sourcepath_linux_test.go:550: forced aux runtime path: aux=127.0.0.1:54774 primary=127.0.0.1:41819 peer=127.0.0.1:55432
    logger.go:105: srcsel-dual-node-IPv4: m1: magicsock: srcsel: data send from source 1 to 127.0.0.1:55432 failed, retrying primary: write: operation not permitted
    sourcepath_linux_test.go:550: forced aux runtime path: aux=[::1]:35075 primary=[::1]:40878 peer=[::1]:55297
    logger.go:105: srcsel-dual-node-IPv6: m1: magicsock: srcsel: data send from source 2 to [::1]:55297 failed, retrying primary: write: operation not permitted
--- PASS: TestSourcePathForcedAuxDualNodeRuntime (0.08s)
    --- PASS: TestSourcePathForcedAuxDualNodeRuntime/IPv4 (0.05s)
    --- PASS: TestSourcePathForcedAuxDualNodeRuntime/IPv6 (0.03s)
    sourcepath_linux_test.go:663: automatic aux runtime path: aux=127.0.0.1:40425 primary=127.0.0.1:52128 peer=127.0.0.1:34470 source={socketID:1 generation:1}
    logger.go:105: srcsel-auto-dual-node-IPv4: m1: magicsock: srcsel: data send from source 1 to 127.0.0.1:34470 failed, retrying primary: write: operation not permitted
    sourcepath_linux_test.go:663: automatic aux runtime path: aux=[::1]:49877 primary=[::1]:37082 peer=[::1]:35898 source={socketID:2 generation:1}
    logger.go:105: srcsel-auto-dual-node-IPv6: m1: magicsock: srcsel: data send from source 2 to [::1]:35898 failed, retrying primary: write: operation not permitted
--- PASS: TestSourcePathAutomaticAuxDualNodeRuntime (0.10s)
    --- PASS: TestSourcePathAutomaticAuxDualNodeRuntime/IPv4 (0.05s)
    --- PASS: TestSourcePathAutomaticAuxDualNodeRuntime/IPv6 (0.05s)
PASS
ok  	tailscale.com/wgengine/magicsock	0.217s
```

## Runtime Conclusion

This runtime revalidation proves the current implementation has reached the
required behavior for both IPv4 and IPv6:

- forced auxiliary data-source selection sends actual WireGuard UDP traffic from
  the auxiliary source socket
- automatic auxiliary data-source selection sends actual WireGuard UDP traffic
  from the selected auxiliary source socket
- IPv4 uses source socket ID `1`
- IPv6 uses source socket ID `2`
- injected auxiliary `EPERM` failure retries the primary socket
- the fallback path is observable through the `retrying primary` log lines
- `lastErrRebind` remains unchanged after successful auxiliary sends
- `lastErrRebind` remains unchanged after auxiliary failure and primary retry

Because the tests store sentinel values in `lastErrRebind` and fail if those
values change, they specifically guard against source selection polluting the
primary rebind error path. The runtime evidence therefore supports continuing
with later cleanup or expansion work without changing the primary rebind
contract.
