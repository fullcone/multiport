# Tailscale Direct Multisource UDP Phase 15 Final Runtime Revalidation

Date: 2026-04-29

This document records the Phase 15 Linux dual-node runtime revalidation for
source-selected direct UDP sends in `fullcone/multiport`.

No production code changed in this phase. This phase re-ran the runtime tests on
the current PR head and records the evidence.

## PR Review Gate

Current PR feedback state before this revalidation:

- PR: `https://github.com/fullcone/multiport/pull/1`
- PR head before this phase:
  `29f764970958dffe14ebdb6ce7b0fea28271931f`
- Local PR recheck time: `2026-04-29T21:01:21.9068198+08:00`
- Latest Phase 14 doc-only review request:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343842629`
- Latest Phase 14 doc-only review response:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343853506`
- Result: no major issues.
- All known inline review threads were resolved.
- No new current unresolved blocking review thread was present, so validation
  continued under the automatic flow rule.

## Objective

Revalidate the runtime behavior after the Phase 14 lazy endpoint guard:

- Forced auxiliary data sends must emit real WireGuard UDP packets from the
  auxiliary socket for IPv4 and IPv6 direct peers.
- Forced auxiliary send failure must fall back to the primary socket for IPv4
  and IPv6.
- Auxiliary data-send errors must not update primary rebind error accounting.
- Automatic auxiliary source selection must still pass the same IPv4 and IPv6
  runtime checks.

## Code Coverage

`wgengine/magicsock/sourcepath_linux_test.go`

- `TestSourcePathForcedAuxDualNodeRuntime`
  - Enables source selection with one auxiliary socket.
  - Forces data sends to the auxiliary source with
    `TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux`.
  - Covers IPv4 and IPv6 subtests.
  - Stores `time.Unix(123, 0)` in `lastErrRebind`.
  - Verifies a successful aux send emits a WireGuard UDP write from the aux
    local address to the current direct peer.
  - Injects `EPERM` for the aux local address.
  - Verifies the failed aux write is attempted first.
  - Verifies fallback emits a successful WireGuard UDP write from the primary
    local address to the same direct peer.
  - Verifies `lastErrRebind` remains the original sentinel after both the
    successful aux send and the fallback path.

- `TestSourcePathAutomaticAuxDualNodeRuntime`
  - Enables automatic data-source selection.
  - Seeds the automatic candidate for the direct peer.
  - Covers IPv4 and IPv6 subtests.
  - Stores `time.Unix(456, 0)` in `lastErrRebind`.
  - Verifies successful aux writes, injected aux failure, primary fallback, and
    unchanged primary rebind error accounting.

- `hasWireGuardWrite`
  - Matches recorded UDP writes by local address, destination address, expected
    error state, and WireGuard packet shape.
  - This is the predicate used by both runtime tests to prove real packet path
    behavior instead of only checking counters.

## Validation Command

Completed local validation on 2026-04-29:

```powershell
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/fullcone -- bash -lc 'go test ./wgengine/magicsock -run "TestSourcePath(ForcedAuxDualNodeRuntime|AutomaticAuxDualNodeRuntime)" -count=1 -v'
```

Validation result:

```text
PASS
ok  	tailscale.com/wgengine/magicsock	0.165s
```

The WSL command printed the known localhost/NAT warning after the Go test
result. The Go test command still exited successfully.

## Runtime Evidence

Forced auxiliary runtime path, IPv4:

```text
=== RUN   TestSourcePathForcedAuxDualNodeRuntime/IPv4
sourcepath_linux_test.go:905: forced aux runtime path: aux=127.0.0.1:55753 primary=127.0.0.1:33189 peer=127.0.0.1:55936
srcsel-dual-node-IPv4: m1: magicsock: srcsel: data send from source 1 to 127.0.0.1:55936 failed, retrying primary: write: operation not permitted
```

Forced auxiliary runtime path, IPv6:

```text
=== RUN   TestSourcePathForcedAuxDualNodeRuntime/IPv6
sourcepath_linux_test.go:905: forced aux runtime path: aux=[::1]:38061 primary=[::1]:54682 peer=[::1]:43487
srcsel-dual-node-IPv6: m1: magicsock: srcsel: data send from source 2 to [::1]:43487 failed, retrying primary: write: operation not permitted
```

Automatic auxiliary runtime path, IPv4:

```text
=== RUN   TestSourcePathAutomaticAuxDualNodeRuntime/IPv4
sourcepath_linux_test.go:1018: automatic aux runtime path: aux=127.0.0.1:56509 primary=127.0.0.1:56198 peer=127.0.0.1:53727 source={socketID:1 generation:1}
srcsel-auto-dual-node-IPv4: m1: magicsock: srcsel: data send from source 1 to 127.0.0.1:53727 failed, retrying primary: write: operation not permitted
```

Automatic auxiliary runtime path, IPv6:

```text
=== RUN   TestSourcePathAutomaticAuxDualNodeRuntime/IPv6
sourcepath_linux_test.go:1018: automatic aux runtime path: aux=[::1]:38497 primary=[::1]:59134 peer=[::1]:37670 source={socketID:2 generation:1}
srcsel-auto-dual-node-IPv6: m1: magicsock: srcsel: data send from source 2 to [::1]:37670 failed, retrying primary: write: operation not permitted
```

Pass summary:

```text
--- PASS: TestSourcePathForcedAuxDualNodeRuntime (0.08s)
--- PASS: TestSourcePathAutomaticAuxDualNodeRuntime (0.06s)
--- PASS: TestSourcePathAutomaticAuxDualNodeRuntime/IPv4 (0.03s)
--- PASS: TestSourcePathAutomaticAuxDualNodeRuntime/IPv6 (0.03s)
PASS
ok  	tailscale.com/wgengine/magicsock	0.165s
```

## Conclusion

Current PR head still proves the intended direct UDP source-selection runtime
behavior:

- Forced auxiliary sends emit real WireGuard UDP packets from auxiliary sockets
  for IPv4 and IPv6.
- Forced auxiliary `EPERM` failures retry through the primary socket for IPv4
  and IPv6.
- Automatic auxiliary source selection remains covered by the same IPv4 and
  IPv6 runtime path.
- Successful aux sends and aux fallback failures do not update
  `lastErrRebind`; the tests keep the sentinel unchanged.
- The Phase 14 lazy endpoint guard remains compatible with the direct endpoint
  runtime path.
