# Tailscale Direct Multisource UDP Phase 15 Final Runtime Revalidation

Date: 2026-04-29

This document records the Phase 15 Linux dual-node runtime revalidation for
source-selected direct UDP sends in `fullcone/multiport`.

No production code changed in this phase. This phase re-ran the runtime tests in
the local PR checkout at `C:\other_project\zerotier-client\multiport` and WSL path
`/mnt/c/other_project/zerotier-client/multiport`; the tested checkout SHA is recorded below.

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

## Phase 15 Feedback Fix Gate

Codex opened one Phase 15 documentation feedback thread after the initial
runtime revalidation record:

- Review request:
  `https://github.com/fullcone/multiport/pull/1#issuecomment-4343916482`
- Inline review thread: `PRRT_kwDOSPBZuM5-cKZI`
- Inline review comment: `PRRC_kwDOSPBZuM68bEbP`
- Feedback: the document recorded the WSL checkout path but did not record the
  actual tested SHA from that checkout.

This update addresses the feedback by recording the tested checkout identity and
re-running the same Linux dual-node runtime validation command with
`git rev-parse HEAD` printed immediately before `go test`.

## Tested Checkout Identity

- Windows checkout: `C:\other_project\zerotier-client\multiport`
- WSL checkout: `/mnt/c/other_project/zerotier-client/multiport`
- Phase 15 PR head before this feedback fix:
  `92c72267198f5b26627d4c287b6ce51297015441`
- The validation command below printed:
  `92c72267198f5b26627d4c287b6ce51297015441`
- Production implementation head before Phase 15 doc-only commits:
  `29f764970958dffe14ebdb6ce7b0fea28271931f`
- `git diff --name-only 29f764970958dffe14ebdb6ce7b0fea28271931f..92c72267198f5b26627d4c287b6ce51297015441`
  listed only
  `docs/tailscale-direct-multisource-udp-phase15-final-runtime-revalidation.md`,
  so the runtime behavior under test came from the same production code as the
  pre-Phase-15 implementation head.

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

Completed local validation on 2026-04-29 from the WSL PR checkout:

```powershell
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/zerotier-client/multiport -- bash -lc 'git rev-parse HEAD && go test ./wgengine/magicsock -run "TestSourcePath(ForcedAuxDualNodeRuntime|AutomaticAuxDualNodeRuntime)" -count=1 -v'
```

Validation result:

```text
92c72267198f5b26627d4c287b6ce51297015441
PASS
ok  	tailscale.com/wgengine/magicsock	0.242s
```

Filtered revalidation evidence from the same checkout:

```powershell
wsl.exe -d Ubuntu-24.04 --cd /mnt/c/other_project/zerotier-client/multiport -- bash -lc 'set -o pipefail; git rev-parse HEAD; go test ./wgengine/magicsock -run "TestSourcePath(ForcedAuxDualNodeRuntime|AutomaticAuxDualNodeRuntime)" -count=1 -v 2>&1 | grep -E "forced aux runtime path|automatic aux runtime path|srcsel: data send|--- PASS|^PASS$|^ok[[:space:]]"'
```

Filtered result:

```text
92c72267198f5b26627d4c287b6ce51297015441
    sourcepath_linux_test.go:905: forced aux runtime path: aux=127.0.0.1:57723 primary=127.0.0.1:39024 peer=127.0.0.1:44044
    logger.go:105: srcsel-dual-node-IPv4: m1: magicsock: srcsel: data send from source 1 to 127.0.0.1:44044 failed, retrying primary: write: operation not permitted
    sourcepath_linux_test.go:905: forced aux runtime path: aux=[::1]:36932 primary=[::1]:46032 peer=[::1]:43797
    logger.go:105: srcsel-dual-node-IPv6: m1: magicsock: srcsel: data send from source 2 to [::1]:43797 failed, retrying primary: write: operation not permitted
--- PASS: TestSourcePathForcedAuxDualNodeRuntime (0.08s)
    --- PASS: TestSourcePathForcedAuxDualNodeRuntime/IPv4 (0.05s)
    --- PASS: TestSourcePathForcedAuxDualNodeRuntime/IPv6 (0.03s)
    sourcepath_linux_test.go:1018: automatic aux runtime path: aux=127.0.0.1:59171 primary=127.0.0.1:58127 peer=127.0.0.1:55477 source={socketID:1 generation:1}
    logger.go:105: srcsel-auto-dual-node-IPv4: m1: magicsock: srcsel: data send from source 1 to 127.0.0.1:55477 failed, retrying primary: write: operation not permitted
    sourcepath_linux_test.go:1018: automatic aux runtime path: aux=[::1]:53524 primary=[::1]:46483 peer=[::1]:41341 source={socketID:2 generation:1}
    logger.go:105: srcsel-auto-dual-node-IPv6: m1: magicsock: srcsel: data send from source 2 to [::1]:41341 failed, retrying primary: write: operation not permitted
--- PASS: TestSourcePathAutomaticAuxDualNodeRuntime (0.12s)
    --- PASS: TestSourcePathAutomaticAuxDualNodeRuntime/IPv4 (0.06s)
    --- PASS: TestSourcePathAutomaticAuxDualNodeRuntime/IPv6 (0.06s)
PASS
ok  	tailscale.com/wgengine/magicsock	0.230s
```

The WSL command printed the known localhost/NAT warning after the Go test
result. The Go test command still exited successfully.

## Runtime Evidence

Forced auxiliary runtime path, IPv4:

```text
=== RUN   TestSourcePathForcedAuxDualNodeRuntime/IPv4
sourcepath_linux_test.go:905: forced aux runtime path: aux=127.0.0.1:57723 primary=127.0.0.1:39024 peer=127.0.0.1:44044
srcsel-dual-node-IPv4: m1: magicsock: srcsel: data send from source 1 to 127.0.0.1:44044 failed, retrying primary: write: operation not permitted
```

Forced auxiliary runtime path, IPv6:

```text
=== RUN   TestSourcePathForcedAuxDualNodeRuntime/IPv6
sourcepath_linux_test.go:905: forced aux runtime path: aux=[::1]:36932 primary=[::1]:46032 peer=[::1]:43797
srcsel-dual-node-IPv6: m1: magicsock: srcsel: data send from source 2 to [::1]:43797 failed, retrying primary: write: operation not permitted
```

Automatic auxiliary runtime path, IPv4:

```text
=== RUN   TestSourcePathAutomaticAuxDualNodeRuntime/IPv4
sourcepath_linux_test.go:1018: automatic aux runtime path: aux=127.0.0.1:59171 primary=127.0.0.1:58127 peer=127.0.0.1:55477 source={socketID:1 generation:1}
srcsel-auto-dual-node-IPv4: m1: magicsock: srcsel: data send from source 1 to 127.0.0.1:55477 failed, retrying primary: write: operation not permitted
```

Automatic auxiliary runtime path, IPv6:

```text
=== RUN   TestSourcePathAutomaticAuxDualNodeRuntime/IPv6
sourcepath_linux_test.go:1018: automatic aux runtime path: aux=[::1]:53524 primary=[::1]:46483 peer=[::1]:41341 source={socketID:2 generation:1}
srcsel-auto-dual-node-IPv6: m1: magicsock: srcsel: data send from source 2 to [::1]:41341 failed, retrying primary: write: operation not permitted
```

Pass summary:

```text
--- PASS: TestSourcePathForcedAuxDualNodeRuntime (0.08s)
--- PASS: TestSourcePathForcedAuxDualNodeRuntime/IPv4 (0.05s)
--- PASS: TestSourcePathForcedAuxDualNodeRuntime/IPv6 (0.03s)
--- PASS: TestSourcePathAutomaticAuxDualNodeRuntime (0.12s)
--- PASS: TestSourcePathAutomaticAuxDualNodeRuntime/IPv4 (0.06s)
--- PASS: TestSourcePathAutomaticAuxDualNodeRuntime/IPv6 (0.06s)
PASS
ok  	tailscale.com/wgengine/magicsock	0.230s
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
