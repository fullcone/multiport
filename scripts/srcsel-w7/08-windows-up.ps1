# 08-windows-up.ps1 — Windows side of the W7 bilateral validation.
#
# Brings up:
#   1. An OpenSSH local port-forward 127.0.0.1:8080 -> remote 127.0.0.1:8080 so
#      Windows tailscaled can reach headscale on the remote.
#   2. A local tailscaled-srcsel.exe in userspace mode.
#   3. tailscale up against the local headscale tunnel endpoint.
#
# Required env vars (read literally from the surrounding shell):
#   SRCSEL_W7_HOST       remote host or IP
#   SRCSEL_W7_USER       remote SSH user (default root)
#   SRCSEL_W7_KEY        absolute path to the SSH private key (push it first
#                        with 07-setup-key.py)
#   SRCSEL_W7_AUTH_KEY   headscale preauth key (hskey-auth-...)
#
# Optional env vars:
#   SRCSEL_W7_BIN_DIR    where to find tailscaled-srcsel.exe + tailscale-srcsel.exe
#                        (default: <repo>/.w7-bins next to the script)
#   SRCSEL_W7_LOG_DIR    where to write tunnel + tailscaled logs
#                        (default: <repo>/.w7-logs)
#   SRCSEL_W7_STATE_DIR  tailscaled state dir
#                        (default: <repo>/.w7-state)
#   SRCSEL_W7_MODE       baseline | force | auto (default baseline)
#   SRCSEL_W7_HOSTNAME   hostname registered with headscale (default srcsel-windows)
#   SRCSEL_W7_TS_PORT    UDP port for tailscaled (default 41642)
#   SRCSEL_W7_FIRST_RUN  set to non-empty on the first run to actually invoke
#                        `tailscale up`; subsequent runs only restart tailscaled

$ErrorActionPreference = "Stop"

function Need($name) {
    $val = [Environment]::GetEnvironmentVariable($name)
    if (-not $val) { throw "env var $name is required" }
    return $val
}
function Default($name, $fallback) {
    $val = [Environment]::GetEnvironmentVariable($name)
    if (-not $val) { return $fallback }
    return $val
}

$host_   = Need "SRCSEL_W7_HOST"
$user    = Default "SRCSEL_W7_USER" "root"
$key     = Need "SRCSEL_W7_KEY"
$authKey = Need "SRCSEL_W7_AUTH_KEY"
$mode    = (Default "SRCSEL_W7_MODE" "baseline").ToLowerInvariant()
if (@("baseline","force","auto") -notcontains $mode) {
    throw "SRCSEL_W7_MODE must be baseline|force|auto (got '$mode')"
}
$hostname = Default "SRCSEL_W7_HOSTNAME" "srcsel-windows"
$tsPort   = Default "SRCSEL_W7_TS_PORT" "41642"

$scriptDir = Split-Path -Parent $PSCommandPath
$repo      = Resolve-Path (Join-Path $scriptDir "..\..")
$bins      = Default "SRCSEL_W7_BIN_DIR"   (Join-Path $repo ".w7-bins")
$logs      = Default "SRCSEL_W7_LOG_DIR"   (Join-Path $repo ".w7-logs")
$state     = Default "SRCSEL_W7_STATE_DIR" (Join-Path $repo ".w7-state")
New-Item -ItemType Directory -Force -Path $logs, $state | Out-Null

# --- 1) tear down any prior session leftovers ------------------------------
Get-Process tailscaled-srcsel -ErrorAction SilentlyContinue | Stop-Process -Force
$prevPidFile = Join-Path $logs "ssh-tunnel.pid"
if (Test-Path $prevPidFile) {
    $oldPid = Get-Content $prevPidFile -ErrorAction SilentlyContinue
    if ($oldPid) { Stop-Process -Id $oldPid -Force -ErrorAction SilentlyContinue }
}
Start-Sleep -Milliseconds 800

# --- 2) start the SSH tunnel ----------------------------------------------
$tunnelLog = Join-Path $logs "ssh-tunnel.log"
$sshArgs = @(
    "-i", $key,
    "-o", "StrictHostKeyChecking=no",
    "-o", "PasswordAuthentication=no",
    "-o", "BatchMode=yes",
    "-o", "ServerAliveInterval=15",
    "-o", "ExitOnForwardFailure=yes",
    "-N",
    "-L", "127.0.0.1:8080:127.0.0.1:8080",
    "$user@$host_"
)
Write-Host "[w7] starting ssh tunnel 127.0.0.1:8080 -> $host_:127.0.0.1:8080"
$sshProc = Start-Process -FilePath "ssh" -ArgumentList $sshArgs -PassThru -NoNewWindow `
    -RedirectStandardOutput $tunnelLog -RedirectStandardError "${tunnelLog}.err"
$sshProc.Id | Out-File -FilePath $prevPidFile -Encoding ascii

Start-Sleep -Seconds 2
try {
    $health = Invoke-WebRequest -Uri "http://127.0.0.1:8080/health" -UseBasicParsing -TimeoutSec 5
    Write-Host "[w7] headscale via tunnel: $($health.Content)"
} catch {
    Write-Host "[w7] tunnel probe failed: $_"
    Get-Content "${tunnelLog}.err" -ErrorAction SilentlyContinue | Select-Object -First 5
    exit 1
}

# --- 3) configure srcsel env for tailscaled --------------------------------
Remove-Item Env:\TS_EXPERIMENTAL_SRCSEL_ENABLE -ErrorAction SilentlyContinue
Remove-Item Env:\TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS -ErrorAction SilentlyContinue
Remove-Item Env:\TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE -ErrorAction SilentlyContinue
Remove-Item Env:\TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE -ErrorAction SilentlyContinue

if ($mode -eq "force") {
    $env:TS_EXPERIMENTAL_SRCSEL_ENABLE = "true"
    $env:TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS = "1"
    $env:TS_EXPERIMENTAL_SRCSEL_FORCE_DATA_SOURCE = "aux"
} elseif ($mode -eq "auto") {
    $env:TS_EXPERIMENTAL_SRCSEL_ENABLE = "true"
    $env:TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS = "1"
    $env:TS_EXPERIMENTAL_SRCSEL_AUTO_DATA_SOURCE = "true"
}

# --- 4) start tailscaled --------------------------------------------------
$tsdLog  = Join-Path $logs "tailscaled-${mode}.log"
$tsdPath = Join-Path $bins "tailscaled-srcsel.exe"
if (-not (Test-Path $tsdPath)) {
    throw "missing $tsdPath (set SRCSEL_W7_BIN_DIR or build with GOOS=windows GOARCH=amd64)"
}
$tsdArgs = @(
    "--tun=userspace-networking",
    "--socket=\\.\pipe\srcsel-w7",
    "--statedir=$state",
    "--port=$tsPort"
)
Write-Host "[w7] starting tailscaled.exe (mode=$mode)"
$tsdProc = Start-Process -FilePath $tsdPath `
    -ArgumentList $tsdArgs -PassThru -NoNewWindow `
    -RedirectStandardOutput $tsdLog -RedirectStandardError "${tsdLog}.err"
$tsdProc.Id | Out-File -FilePath (Join-Path $logs "tailscaled.pid") -Encoding ascii
Start-Sleep -Seconds 4

# --- 5) (first run only) tailscale up --------------------------------------
$tsCli = Join-Path $bins "tailscale-srcsel.exe"
$socket = "--socket=\\.\pipe\srcsel-w7"
if ($env:SRCSEL_W7_FIRST_RUN) {
    Write-Host "[w7] tailscale up (first run)"
    & $tsCli $socket up `
        --login-server="http://127.0.0.1:8080" `
        --auth-key=$authKey `
        --hostname=$hostname `
        --accept-routes=$false `
        --accept-dns=$false `
        --unattended 2>&1 | Tee-Object -FilePath (Join-Path $logs "tailscale-up.log")
}

# --- 6) status + sockets --------------------------------------------------
Write-Host ""
Write-Host "[w7] tailscale status:"
& $tsCli $socket status 2>&1 | Select-Object -First 10

Write-Host ""
Write-Host "[w7] aux + primary UDP sockets owned by tailscaled.exe:"
Get-NetUDPEndpoint -OwningProcess $tsdProc.Id -ErrorAction SilentlyContinue |
    Where-Object { $_.LocalPort -ne 0 } |
    Sort-Object LocalAddress, LocalPort |
    Format-Table -AutoSize
