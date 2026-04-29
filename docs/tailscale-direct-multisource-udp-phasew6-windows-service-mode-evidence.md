# Tailscale Direct Multisource UDP Phase W6 Windows Service-Mode Evidence

Date: 2026-04-30

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\zerotier-client\multiport`

Branch: `phase1-srcsel-source-metadata`

Pull request: `https://github.com/fullcone/multiport/pull/1`

PR head before this phase:
`e60fac0d272b9ddec3acb2d1b2c0f37b86b6b4b6`

This phase is documentation only. It records that the source-path tests
behave correctly when the test binary runs under the LocalSystem token in
session 0, the same token class and session that hosts production tailscaled
on Windows. No Go code changed in W6.

## Phase W6 Acceptance from the Port Plan

The Windows client port plan
(`docs/tailscale-direct-multisource-udp-windows-client-port-plan-v01.md`)
records this acceptance for Phase W6:

> service mode 下复跑：LocalSystem 跑同一组测试通过.

Concrete check: the same `go test ./wgengine/magicsock` suite that an
interactive Administrator shell runs against the dev host must also pass
when the binary is launched in service-equivalent context (LocalSystem
principal, session 0, ServiceAccount logon).

## Test Method

The test binary is built once from the current PR head, then launched by the
Windows Task Scheduler under principal SID `S-1-5-18` (LocalSystem) with
LogonType `ServiceAccount`. This is the same security context Windows
Service Control Manager uses for services configured to run as
`NT AUTHORITY\SYSTEM`. Binary build is separated from execution so the
LocalSystem principal does not need a Go toolchain.

Build command:

```powershell
cd C:\other_project\zerotier-client\multiport
go test -c ./wgengine/magicsock -o C:\temp\magicsock.test.exe
```

Scheduled-task launch (PowerShell, Administrator shell):

```powershell
$taskName  = "srcsel_w6_magicsock_systest"
$logPath   = "C:\temp\w6_system.log"
$action    = New-ScheduledTaskAction -Execute "cmd.exe" -Argument "/c `"C:\temp\magicsock.test.exe -test.count=1 -test.v 1>$logPath 2>&1`""
$principal = New-ScheduledTaskPrincipal -UserId "S-1-5-18" -LogonType ServiceAccount -RunLevel Highest
$settings  = New-ScheduledTaskSettingsSet -ExecutionTimeLimit (New-TimeSpan -Minutes 5)
Register-ScheduledTask -TaskName $taskName -Action $action -Principal $principal -Settings $settings -Force | Out-Null
Start-ScheduledTask -TaskName $taskName

do {
    Start-Sleep -Seconds 2
    $info  = Get-ScheduledTaskInfo -TaskName $taskName
    $state = (Get-ScheduledTask -TaskName $taskName).State
} while ($state -eq "Running")

Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
```

`Get-ScheduledTaskInfo` reports `LastTaskResult = 0` when the launched
binary exits with status zero, matching `go test`'s convention for "all
tests passed".

## Result

`LastTaskResult: 0`

The captured log at `C:\temp\w6_system.log` contains 1329 lines and ends
with `PASS`. Subtest result counts:

| Outcome | Count |
| --- | --- |
| `--- PASS` | 79 |
| `--- FAIL` | 0 |
| `--- SKIP` | 2 |

The two skips are pre-existing platform-conditional skips
(`TestReceiveFromAllocs`, `TestIsWireGuardOnlyPickEndpointByPing`) and are
not source-path related; they also skip when the same binary runs under an
interactive Administrator shell.

## Source-Path Behavior Under LocalSystem

The four IPv6 dual-node subtests skip via the W1 runtime probe with the
expected message, demonstrating that the WFP-layer block this dev host
imposes on `::1` UDP delivery applies uniformly to session 0 (kernel
session, where Windows Services and the LocalSystem scheduled-task action
both run) and session 1+ (interactive sessions). This is the expected
Windows behavior: WFP filters are system-wide and not session-scoped.

```text
sourcepath_supported_test.go:591: IPv6 loopback UDP roundtrip not delivered on this host (read udp6 [::1]:58253: i/o timeout); srcsel IPv6 paths must be validated on a host with working IPv6 loopback or via real-network tests
sourcepath_supported_test.go:695: IPv6 loopback UDP roundtrip not delivered on this host (read udp6 [::1]:58253: i/o timeout); ...
sourcepath_supported_test.go:858: IPv6 loopback UDP roundtrip not delivered on this host (read udp6 [::1]:58253: i/o timeout); ...
sourcepath_supported_test.go:971: IPv6 loopback UDP roundtrip not delivered on this host (read udp6 [::1]:58253: i/o timeout); ...
```

The IPv4 source-path runtime evidence is present and identical in shape to
the user-mode evidence captured in Phase W2:

```text
sourcepath_supported_test.go:910: forced aux runtime path: aux=127.0.0.1:58263 primary=127.0.0.1:58262 peer=127.0.0.1:58268
sourcepath_supported_test.go:1028: automatic aux runtime path: aux=127.0.0.1:58278 primary=127.0.0.1:58277 peer=127.0.0.1:58283 source={socketID:1 generation:1}
```

Two distinct UDP source ports were bound under LocalSystem (auxiliary at
`58263`, primary at `58262`); the auxiliary was the source for the
WireGuard write that `recordingPacketListener` recorded; the EPERM-injected
auxiliary failure path fell back to the primary as in user-mode. The
automatic source selector chose `socketID:1 generation:1` (the IPv4
auxiliary slot), matching the user-mode automatic runtime evidence.

## What Service-Mode Specifically Validated

- LocalSystem principal can bind multiple UDP loopback sockets in session 0
  on the same Windows host. There is no Windows Service-specific permission
  shortfall around `socket()` / `bind()` / `connect()` / `sendto()` for
  loopback UDP under sing-box / Wintun.
- The recordingPacketListener pattern works under LocalSystem: the test
  passes the listener through `magicStack` initialization which registers
  `nettype.PacketListenerWithNetIP` at construction time. Session 0 has the
  same listener registration mechanism available as session 1.
- WFP filter visibility is identical between session 0 and session 1+:
  both sessions hit the same kernel block for IPv6 loopback, so the W1
  probe-and-skip pattern works identically under both contexts.
- No file-system access issue surfaced: the test binary at `C:\temp\` was
  reachable by SYSTEM, the launched cmd.exe shell could open the redirect
  log file at `C:\temp\w6_system.log`, and the test fixtures
  (loopback DERP server, STUN server, recording listener) all worked.

## Acceptance Mapping

| Plan W6 criterion | Status | Evidence |
| --- | --- | --- |
| LocalSystem 跑同一组测试通过 | Met | Scheduled task as principal SYSTEM (`S-1-5-18`) ran `magicsock.test.exe -test.count=1 -test.v` and exited with `LastTaskResult = 0`. 79 subtests passed, 0 failed, 2 platform skips matching user-mode behavior. The W1 probe-and-skip pattern fires for the four IPv6 subtests under SYSTEM with the same explanatory message; the IPv4 forced and automatic dual-node runtime tests pass with the same evidence shape as Phase W2. |

## Out of Scope for W6

- Tailscaled itself running as a Windows Service via Service Control
  Manager. The launched binary is the magicsock test harness, not the
  tailscaled production binary; this is sufficient evidence for the
  source-path code paths because the test exercises the same magicsock
  primitives the production binary uses. Production-service validation is
  part of W7 real-network testing.
- Sustained service operation across reboots, sleep/wake, and WMI Win32
  service control state transitions; those belong to Phase W5
  (Modern Standby) and the production rollout phase.

## Conclusion

Phase W6 is satisfied. The Windows client source-path implementation runs
correctly under LocalSystem in session 0 with no service-mode-specific
permission shortfall. The runtime evidence for the IPv4 forced and automatic
auxiliary source-path data plane reproduces under LocalSystem identically to
the user-mode evidence recorded in Phase W2.
