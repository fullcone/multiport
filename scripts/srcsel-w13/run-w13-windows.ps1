# run-w13-windows.ps1 — Windows side of the W13 three-node coordinated run.
#
# Pair this with `python3 w13-linux.py --mode <same>` started at roughly
# the same time on the dev box. The two halves do not synchronize over
# the network; the operator picks the mode for both. W13 reuses the
# W12 mesh (Windows + 216 + 36).
#
# Usage (from the W12 pack folder, which has the binaries + state):
#
#   .\run-w13-windows.ps1 -Mode baseline -PackPath E:\w12-pack
#   .\run-w13-windows.ps1 -Mode force    -PackPath E:\w12-pack
#   .\run-w13-windows.ps1 -Mode auto     -PackPath E:\w12-pack
#
# What it does for one mode:
#   1. Stops any running tailscaled-srcsel.exe.
#   2. Sets the TS_EXPERIMENTAL_SRCSEL_* env knobs for the chosen mode.
#   3. Starts tailscaled-srcsel.exe in userspace-networking mode against
#      the existing W12-pack state directory. Reuses the existing node
#      identity, so no `tailscale up` and no header `register`
#      side-effects on every run.
#   4. Waits 8 s for warmup.
#   5. Discovers the two Linux peers (host + nat) from
#      `tailscale status --json`.
#   6. Runs sustained TSMP pings (default 40 per direction) from the
#      Windows side to host (v4 + v6) and to nat (v4 only — 36 is not
#      dual-stack). Times out gracefully on no-reply.
#   7. Samples magicsock_srcsel_* metrics on the Windows side.
#   8. Prints a labelled transcript suitable for combining with
#      `w13-linux.py`'s transcript.
#
# Requirements:
#   - The W12 pack folder must already exist with tailscaled-srcsel.exe
#     and tailscale-srcsel.exe inside, and a populated `state/` from a
#     prior W12 run. If the state dir is empty, run the W12 driver
#     once first to register, then come back.
#   - PowerShell 5+ (Windows 10/11/Server 2019+).
#   - Outbound to 216.144.236.235 + 36.111.166.166 reachable.

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("baseline", "force", "auto")]
    [string]$Mode,

    [Parameter(Mandatory = $true)]
    [string]$PackPath,

    [int]$Pings = 40,

    [int]$Timeout = 10,

    [string]$HostPeer = "srcsel-pair2-host",
    [string]$NatPeer  = "srcsel-w12-nat-server"
)

$ErrorActionPreference = "Stop"
$tsdPath = Join-Path $PackPath "tailscaled-srcsel.exe"
$tsCli   = Join-Path $PackPath "tailscale-srcsel.exe"
$state   = Join-Path $PackPath "state"
$logs    = Join-Path $PackPath "logs"
foreach ($p in @($tsdPath, $tsCli)) {
    if (-not (Test-Path $p)) { throw "missing $p" }
}
if (-not (Test-Path $state)) {
    throw ("$state does not exist. Run the W12 driver (run-w12.ps1) " +
           "once first to register the Windows node, then re-run W13.")
}
New-Item -ItemType Directory -Force -Path $logs | Out-Null

function Invoke-TS {
    $prev = $ErrorActionPreference
    $ErrorActionPreference = "Continue"
    try {
        & $tsCli "--socket=\\.\pipe\srcsel-w13" @args 2>&1 | ForEach-Object {
            if ($_ -is [System.Management.Automation.ErrorRecord]) { $_.Exception.Message }
            else { "$_" }
        }
    } finally {
        $ErrorActionPreference = $prev
    }
}

function Stop-Tailscaled {
    Get-Process tailscaled-srcsel -ErrorAction SilentlyContinue |
        Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 800
}

function Start-Tailscaled([string]$ModeName) {
    Stop-Tailscaled
    Remove-Item Env:\TS_EXPERIMENTAL_SRCSEL_ENABLE -ErrorAction SilentlyContinue
    Remove-Item Env:\TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS -ErrorAction SilentlyContinue
    Remove-Item Env:\TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE -ErrorAction SilentlyContinue
    Remove-Item Env:\TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE -ErrorAction SilentlyContinue
    if ($ModeName -eq "force") {
        $env:TS_EXPERIMENTAL_SRCSEL_ENABLE = "true"
        $env:TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS = "1"
        $env:TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE = "aux"
    } elseif ($ModeName -eq "auto") {
        $env:TS_EXPERIMENTAL_SRCSEL_ENABLE = "true"
        $env:TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS = "1"
        $env:TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE = "true"
    }

    $tsdLog = Join-Path $logs "tailscaled-w13-${ModeName}.log"
    $tsdArgs = @(
        "--tun=userspace-networking",
        "--socket=\\.\pipe\srcsel-w13",
        "--statedir=$state",
        "--port=41642"
    )
    $proc = Start-Process -FilePath $tsdPath -ArgumentList $tsdArgs -PassThru -NoNewWindow `
        -RedirectStandardOutput $tsdLog -RedirectStandardError "${tsdLog}.err"
    Start-Sleep -Seconds 8
    return $proc
}

function Get-PeerIPs([string]$PeerName) {
    $json = Invoke-TS status --json
    $obj = $json | ConvertFrom-Json
    foreach ($k in $obj.Peer.PSObject.Properties.Name) {
        $p = $obj.Peer.$k
        if ($p.HostName -eq $PeerName) {
            return ,$p.TailscaleIPs
        }
    }
    return @()
}

Write-Host ""
Write-Host "##### W13 WINDOWS | mode = $Mode #####"
Write-Host "  pack         : $PackPath"
Write-Host "  pings/dir    : $Pings"
Write-Host "  per-ping to  : ${Timeout}s"

$proc = Start-Tailscaled $Mode

Write-Host ""
Write-Host "--- envknobs in tailscaled log:"
Get-Content (Join-Path $logs "tailscaled-w13-${Mode}.log.err") -ErrorAction SilentlyContinue |
    Select-String -Pattern "envknob: TS_EXPERIMENTAL_SRCSEL|magicsock: disco key" |
    Select-Object -First 6 |
    ForEach-Object { Write-Host "  $($_.Line)" }

Write-Host ""
Write-Host "--- aux + primary UDP sockets owned by pid=$($proc.Id):"
Get-NetUDPEndpoint -OwningProcess $proc.Id -ErrorAction SilentlyContinue |
    Where-Object { $_.LocalPort -ne 0 } |
    Sort-Object LocalAddress, LocalPort |
    Format-Table -AutoSize | Out-String | Write-Host

Write-Host ""
Write-Host "--- tailscale status (top 4):"
Invoke-TS status | Select-Object -First 4 | ForEach-Object { Write-Host "  $_" }

$hostIPs = Get-PeerIPs $HostPeer
$natIPs  = Get-PeerIPs $NatPeer
$hostV4 = ($hostIPs | Where-Object { $_ -like "*.*" }) | Select-Object -First 1
$hostV6 = ($hostIPs | Where-Object { $_ -like "*:*" }) | Select-Object -First 1
$natV4  = ($natIPs  | Where-Object { $_ -like "*.*" }) | Select-Object -First 1

Write-Host ""
Write-Host "--- discovered peer addrs:"
Write-Host "  host: v4=$hostV4  v6=$hostV6"
Write-Host "  nat : v4=$natV4   v6=(n/a, 36 is IPv4-only)"

foreach ($t in @(
    @{Label="win -> host (v4)"; Dst=$hostV4},
    @{Label="win -> host (v6)"; Dst=$hostV6},
    @{Label="win -> nat  (v4)"; Dst=$natV4}
)) {
    if (-not $t.Dst) { continue }
    Write-Host ""
    Write-Host "--- TSMP $($t.Label) (-> $($t.Dst))"
    Invoke-TS ping --tsmp --c=$Pings --timeout=${Timeout}s $t.Dst |
        ForEach-Object { Write-Host "  $_" }
}

Write-Host ""
Write-Host "--- metrics win ($Mode):"
Invoke-TS debug metrics |
    Select-String -Pattern "^magicsock_srcsel" |
    ForEach-Object { Write-Host "  $($_.Line)" }

Write-Host ""
Write-Host "##### W13 WINDOWS | done | mode = $Mode #####"
