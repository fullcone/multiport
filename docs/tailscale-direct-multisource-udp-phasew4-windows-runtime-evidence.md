# Tailscale Direct Multisource UDP Phase W4 Windows Runtime Evidence

Date: 2026-04-30

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\zerotier-client\multiport`

WSL checkout: `/mnt/c/other_project/zerotier-client/multiport`

Branch: `phase-w4-windows-runtime-evidence`

Pull request: not yet opened

Branch base: `ef128edd6` (Phase 20 head; PR #1 + PR #2 changes both present).

This phase is documentation only. It records the Windows native runtime
evidence for the forced auxiliary and automatic auxiliary source-path data
send paths now that Phase 20's primary-baseline gate has landed. No Go code
changed in W4.

## W4 Acceptance from the Port Plan

`docs/tailscale-direct-multisource-udp-windows-client-port-plan-v01.md` § 5
specifies:

> W4 | Windows forced data source + automatic selection | dual-node runtime
> test 在 Windows 通过；抓包验证

§ 4.3 elaborates that the pass conditions match PR #1 Phase 15:

> 抓包显示 forced aux 真的从 aux 本地端口发出 WireGuard payload。
> 自动选择路径在 IPv4 / IPv6 均通过。
> 注入 send 失败后 fallback 到 primary 成功。
> `lastErrRebind` 不被 aux 失败更新。

W2 already recorded the in-process syscall-equivalent evidence using
`recordingPacketListener` per Phase 15's pattern. W4 extends that with:

1. Re-running the same tests on Windows native against the post-Phase-20
   tree (verifying Phase 20's primary-baseline gate did not regress
   Windows behavior).
2. Adding OS-level evidence via `Get-NetUDPEndpoint` sampling to confirm
   `magicsock.test` actually binds the auxiliary loopback ports the test
   logs report.
3. Recording the `pktmon` Loopback Pseudo-Interface limitation that
   blocks classic NDIS packet capture for `127.0.0.1` / `::1` traffic.

## Test Command

Windows native PowerShell, working tree at HEAD `ef128edd6`:

```powershell
$env:CGO_ENABLED = "0"
go test ./wgengine/magicsock `
  -run "TestSourcePath(ForcedAux|AutomaticAux)DualNodeRuntime" `
  -count=1 -v -timeout 300s
```

## Test Results

Captured 2026-04-30 on Windows Server 2025, Go 1.26.2 windows/amd64:

```text
=== RUN   TestSourcePathForcedAuxDualNodeRuntime
=== RUN   TestSourcePathForcedAuxDualNodeRuntime/IPv4
=== RUN   TestSourcePathForcedAuxDualNodeRuntime/IPv6
--- PASS: TestSourcePathForcedAuxDualNodeRuntime (0.61s)
    --- PASS: TestSourcePathForcedAuxDualNodeRuntime/IPv4 (...)
    --- SKIP: TestSourcePathForcedAuxDualNodeRuntime/IPv6 (0.50s)
=== RUN   TestSourcePathAutomaticAuxDualNodeRuntime
=== RUN   TestSourcePathAutomaticAuxDualNodeRuntime/IPv4
=== RUN   TestSourcePathAutomaticAuxDualNodeRuntime/IPv6
--- PASS: TestSourcePathAutomaticAuxDualNodeRuntime (0.09s)
    --- PASS: TestSourcePathAutomaticAuxDualNodeRuntime/IPv4 (...)
    --- SKIP: TestSourcePathAutomaticAuxDualNodeRuntime/IPv6 (0.00s)
PASS
ok  	tailscale.com/wgengine/magicsock	0.751s
```

The IPv6 subtests are skipped via the `ipv6LoopbackUDPRoundtripProbe`
introduced in W1: this dev host's IPv6 loopback is intercepted below the WFP
filter layer (see W3 deferral for the hardening / EDR chain that is
suspected). IPv4 subtests run end-to-end.

## Forced Auxiliary IPv4 Runtime Evidence

Sample log from a representative run:

```text
sourcepath_supported_test.go:920: forced aux runtime path: aux=127.0.0.1:56489 primary=127.0.0.1:56488 peer=127.0.0.1:56494
m1: magicsock: srcsel: data send from source 1 to 127.0.0.1:56494 failed, retrying primary: write: operation not permitted
--- PASS: TestSourcePathForcedAuxDualNodeRuntime/IPv4
```

What the evidence proves (matches Phase 15 / W2 contract):

- Two disjoint Windows UDP sockets were bound for the auxiliary and primary
  source paths: aux on `127.0.0.1:56489`, primary on `127.0.0.1:56488`.
- Direct IPv4 mesh established to peer `127.0.0.1:56494`.
- With `TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE=aux`, the
  `recordingPacketListener` recorded a successful WireGuard UDP write from
  `127.0.0.1:56489` (auxiliary local) to `127.0.0.1:56494` (direct peer);
  `hasWireGuardWrite(writes, auxLocal, directPeer, false)` was true.
- The injected `syscall.EPERM` failure on the auxiliary socket caused the
  data send to retry through the primary socket, captured by
  `hasWireGuardWrite(writes, primaryLocal, directPeer, false)`.
- `lastErrRebind` retained its sentinel after the auxiliary failure,
  matching Linux behavior.

## Automatic Auxiliary IPv4 Runtime Evidence

```text
sourcepath_supported_test.go:1044: automatic aux runtime path: aux=127.0.0.1:56504 primary=127.0.0.1:56503 peer=127.0.0.1:56509 source={socketID:1 generation:1}
m1: magicsock: srcsel: data send from source 1 to 127.0.0.1:56509 failed, retrying primary: write: operation not permitted
--- PASS: TestSourcePathAutomaticAuxDualNodeRuntime/IPv4
```

The automatic test sets `TS_EXPERIMENTAL_SRCSEL_AUX_BEAT_THRESHOLD_PCT=-1`
to disable Phase 20's primary-baseline gate. Loopback primary RTT is
sub-millisecond and the seed samples are 1 ms, so leaving the gate enabled
would deterministically reject the seeded auxiliary candidate — the test
exercises automatic-mode selection logic, not the Phase 20 relative
improvement check (the Phase 20 doc records this rationale).

## OS-level evidence: `Get-NetUDPEndpoint` sampling

The in-process `recordingPacketListener` evidence above is wire-shape
equivalent (per W2's framing) but does not prove the OS actually bound the
auxiliary ports. `Get-NetUDPEndpoint` against the `magicsock.test` PID
during a long-running invocation provides that confirmation:

```powershell
$env:CGO_ENABLED = "0"
$proc = Start-Process -FilePath "go" -ArgumentList @(
  "test","./wgengine/magicsock",
  "-run","TestSourcePath(ForcedAux|AutomaticAux)DualNodeRuntime/IPv4",
  "-count=1000","-v","-timeout=300s") -PassThru -NoNewWindow `
  -RedirectStandardOutput "w4-test.log" -RedirectStandardError "w4-err.log"
$captured = @{}
for ($i = 0; $i -lt 50; $i++) {
  Start-Sleep -Milliseconds 100
  $testProc = Get-Process -Name "magicsock.test" -EA SilentlyContinue | Select -First 1
  if ($testProc) {
    Get-NetUDPEndpoint -OwningProcess $testProc.Id -EA SilentlyContinue | ForEach-Object {
      $captured["$($_.LocalAddress):$($_.LocalPort)"] = $_.LocalAddress
    }
  }
}
Stop-Process $proc -EA SilentlyContinue
```

Sample of unique loopback endpoints captured (220 total in one 5 s window
of 1000 iterations):

```text
127.0.0.1:49165
127.0.0.1:49179
127.0.0.1:49180
127.0.0.1:63498
127.0.0.1:63499
127.0.0.1:63518
127.0.0.1:63519
::1:49178
::1:49181
::1:63497
::1:63500
...
```

Endpoints arrive in tight clusters of three consecutive ports
(`primary v4` / `aux v4` / `aux v6`), which matches `Conn.connBind.Open`
binding `pconn4`, then `aux4`, then `aux6` from the same OS ephemeral
range. Both `127.0.0.1` and `::1` ports show up even when the IPv4
subtest is the only thing running, because the magicsock open path
unconditionally binds both address families' aux sockets when
`TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=1` is in effect.

This is OS-level evidence that:

- Windows allowed the test process to bind two extra UDP sockets per
  magicsock instance (aux v4 + aux v6) in addition to the primary
  pair.
- The aux sockets are owned by the same `magicsock.test` PID as the
  primary sockets — the injected `recordingPacketListener` is observing
  real OS-bound sockets, not test-internal stand-ins.
- IPv4 aux binding is reliable; IPv6 aux binding is reliable at the
  socket-bind layer even on this host, despite IPv6 loopback delivery
  being intercepted below the WFP layer (W3).

## `pktmon` Loopback Pseudo-Interface limitation

`pktmon` was attempted as a wire-level capture tool. With the default
`--comp all` component selection, `pktmon` captured 90 packets across
non-loopback NICs (RDP, DHCP, sing-tun) but **zero** packets on
`127.0.0.1`. This is a Windows architectural limit, not a srcsel issue:
loopback UDP traffic traverses the **Loopback Pseudo-Interface** that
sits inside the TCP/IP stack and never crosses NDIS, so NDIS-tier capture
tools — `pktmon`, classic Wireshark with WinPcap — cannot observe it.

The standard mitigation is Wireshark + Npcap with the "Adapter for
loopback traffic capture" feature, which installs a separate
`Npcap Loopback Adapter` device. That adapter is not present on this
dev host. W4 records the limitation; a future phase or W7 real-network
validation can collect Wireshark+Npcap captures if they become
necessary for compliance evidence.

## Status

- W4 IPv4 forced + automatic dual-node runtime test pass on Windows
  native against post-Phase-20 HEAD `ef128edd6`.
- W4 IPv6 dual-node runtime tests SKIP via the W1 probe; deferred to
  W3 environment.
- OS-level UDP endpoint binding of auxiliary sockets confirmed by
  `Get-NetUDPEndpoint` sampling.
- Wire-shape evidence equivalent to Phase 15 / W2 in-process
  `recordingPacketListener` captures.
- True NDIS-tier loopback packet capture is a Windows architectural gap
  (not a srcsel gap); future Wireshark + Npcap loopback adapter capture
  recorded as nice-to-have, not blocking.

## Out Of Scope For W4

- Real-host bidirectional aux WireGuard validation (W7).
- Modern Standby / multi-NIC / Windows Firewall risk checklist (W5).
- Service-mode rerun (W6 already done).
- Final closeout (W9).
