<#
.SYNOPSIS
  Boot smoke test for the Windows LoopWorker binary. Proves the shipped
  artifact actually STARTS, SERVES /api/v1/health, and SHUTS DOWN GRACEFULLY —
  which is the gate that was missing when CI only ran `loopworker version`
  (a binary that could not start got shipped).

.DESCRIPTION
  1. Starts bin\loopworker.exe on a free port with an isolated temp data dir.
  2. Polls GET /api/v1/health until HTTP 200 (or fails with the captured log).
  3. Sends CTRL_BREAK_EVENT to the child's process group. Go maps Ctrl+Break to
     syscall.SIGTERM on Windows, so this exercises the same graceful-shutdown
     path that `docker stop` exercises on Linux.
  4. Asserts the process exits 0 within -GracefulTimeoutSec.

  Exit codes: 0 = all gates passed, 1 = boot/health/shutdown gate failed,
  2 = harness problem (binary missing, no console for ctrl event, etc).

  Note: if the script runs without a console (e.g. some CI hosts) step 3 cannot
  deliver a ctrl event; the script then FAILS rather than silently skipping the
  shutdown assertion, because a false green here is exactly the bug class this
  file exists to prevent. Use -SkipGraceful only for local iteration.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$BinPath,
    [int]$HealthPath = 0,          # unused placeholder kept for arg compatibility
    [string]$Port = '',
    [int]$BootTimeoutSec = 45,
    [int]$GracefulTimeoutSec = 20,
    [switch]$SkipGraceful
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path $BinPath)) { Write-Host "FATAL: binary not found: $BinPath" -ForegroundColor Red; exit 2 }
$BinPath = (Resolve-Path $BinPath).Path

# ---- P/Invoke: create the child in its own process group, then ctrl-break it --
if (-not ('LW.Smoke' -as [type])) {
Add-Type -Namespace 'LW' -Name 'Smoke' -MemberDefinition @'
    [System.Runtime.InteropServices.DllImport("kernel32.dll", SetLastError = true, CharSet = System.Runtime.InteropServices.CharSet.Unicode)]
    public static extern bool CreateProcessW(
        string lpApplicationName, string lpCommandLine,
        IntPtr lpProcessAttributes, IntPtr lpThreadAttributes,
        bool bInheritHandles, uint dwCreationFlags,
        IntPtr lpEnvironment, string lpCurrentDirectory,
        ref STARTUPINFO lpStartupInfo, out PROCESS_INFORMATION lpProcessInformation);

    [System.Runtime.InteropServices.DllImport("kernel32.dll", SetLastError = true)]
    public static extern uint WaitForSingleObject(IntPtr hProcess, uint dwMilliseconds);

    [System.Runtime.InteropServices.DllImport("kernel32.dll", SetLastError = true)]
    public static extern bool GetExitCodeProcess(IntPtr hProcess, out uint lpExitCode);

    [System.Runtime.InteropServices.DllImport("kernel32.dll", SetLastError = true)]
    public static extern bool CloseHandle(IntPtr hObject);

    [System.Runtime.InteropServices.DllImport("kernel32.dll", SetLastError = true)]
    public static extern bool GenerateConsoleCtrlEvent(uint dwCtrlEvent, uint dwProcessGroupId);

    [System.Runtime.InteropServices.StructLayout(System.Runtime.InteropServices.LayoutKind.Sequential)]
    public struct STARTUPINFO {
        public uint cb; public string lpReserved; public string lpDesktop; public string lpTitle;
        public uint dwX, dwY, dwXSize, dwYSize, dwXCountChars, dwYCountChars, dwFillAttribute, dwFlags;
        public ushort wShowWindow, cbReserved2;
        public IntPtr lpReserved2, hStdInput, hStdOutput, hStdError;
    }

    [System.Runtime.InteropServices.StructLayout(System.Runtime.InteropServices.LayoutKind.Sequential)]
    public struct PROCESS_INFORMATION {
        public IntPtr hProcess; public IntPtr hThread; public uint dwProcessId; public uint dwThreadId;
    }
'@
}

$CREATE_NEW_PROCESS_GROUP = 0x00000200
$CTRL_BREAK_EVENT = 1
$WAIT_OBJECT_0 = 0
$WAIT_TIMEOUT = 0x102

if ($Port -eq '') {
    # Grab an ephemeral free port so parallel jobs never collide.
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $Port = $listener.LocalEndpoint.Port
    $listener.Stop()
}

$dataDir    = Join-Path $env:TEMP ("lw-smoke-data-"   + [guid]::NewGuid().ToString('N'))
$pluginsDir = Join-Path $env:TEMP ("lw-smoke-plugins-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $dataDir, $pluginsDir | Out-Null

# Env overrides are read by internal/config.applyEnvironmentOverrides().
$env:LOOPWORKER_PORT       = "$Port"
$env:LOOPWORKER_DATA_DIR   = $dataDir
$env:LOOPWORKER_PLUGINS_DIR= $pluginsDir
$env:LOOPWORKER_LOG_LEVEL  = 'debug'

$si = New-Object LW.Smoke+STARTUPINFO
$si.cb = [System.Runtime.InteropServices.Marshal]::SizeOf($si)
$pi = New-Object LW.Smoke+PROCESS_INFORMATION
$cmdLine = "`"$BinPath`""

Write-Host "==> booting $BinPath on 127.0.0.1:$Port" -ForegroundColor Cyan
$ok = [LW.Smoke]::CreateProcessW($null, $cmdLine, [IntPtr]::Zero, [IntPtr]::Zero,
        $false, $CREATE_NEW_PROCESS_GROUP, [IntPtr]::Zero, (Split-Path -Parent $BinPath),
        [ref]$si, [ref]$pi)
if (-not $ok) {
    Write-Host "FATAL: CreateProcessW failed: $([System.Runtime.InteropServices.Marshal]::GetLastWin32Error())" -ForegroundColor Red
    exit 2
}

$pid2 = $pi.dwProcessId
$fail = {
    param([string]$msg, [int]$code)
    Write-Host "FAIL: $msg" -ForegroundColor Red
    Write-Host "     child pid=$pid2 — log tail above (stdout/stderr inherited the console)" -ForegroundColor DarkGray
    [LW.Smoke]::CloseHandle($pi.hThread) | Out-Null
    [LW.Smoke]::CloseHandle($pi.hProcess) | Out-Null
    exit $code
}

try {
    # ---------------- gate 1: process still alive shortly after exec ----------
    Start-Sleep -Seconds 2
    [uint32]$ec = 0
    [LW.Smoke]::GetExitCodeProcess($pi.hProcess, [ref]$ec) | Out-Null
    if ($ec -ne 259) {  # 259 = STILL_ACTIVE
        & $fail "process exited immediately with code $ec before serving anything (this is the crash-loop class of bug the old CI shipped)" 1
    }

    # ---------------- gate 2: /api/v1/health answers 200 ----------------------
    $url = "http://127.0.0.1:$Port/api/v1/health"
    $deadline = (Get-Date).AddSeconds($BootTimeoutSec)
    $healthy = $false
    $body = ''
    while ((Get-Date) -lt $deadline) {
        try {
            $resp = Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 3
            if ($resp.StatusCode -eq 200) { $body = $resp.Content; $healthy = $true; break }
        } catch { Start-Sleep -Milliseconds 500 }
        # re-check liveness so a crash mid-poll reports the real cause
        [LW.Smoke]::GetExitCodeProcess($pi.hProcess, [ref]$ec) | Out-Null
        if ($ec -ne 259) { & $fail "process died (exit $ec) before health passed" 1 }
    }
    if (-not $healthy) { & $fail "GET $url did not return 200 within ${BootTimeoutSec}s" 1 }
    Write-Host "    OK  health 200: $body" -ForegroundColor Green

    if ($SkipGraceful) { Write-Host '    SKIP graceful-shutdown gate (-SkipGraceful)' -ForegroundColor Yellow }
    else {
        # ---------------- gate 3: graceful shutdown via SIGTERM path ----------
        $sent = [LW.Smoke]::GenerateConsoleCtrlEvent($CTRL_BREAK_EVENT, $pid2)
        if (-not $sent) {
            & $fail "GenerateConsoleCtrlEvent failed ($([System.Runtime.InteropServices.Marshal]::GetLastWin32Error())) — cannot prove graceful shutdown without a console; run this in an interactive/Console-enabled context instead of pretending it passed" 2
        }
        $wait = [LW.Smoke]::WaitForSingleObject($pi.hProcess, [uint32]($GracefulTimeoutSec * 1000))
        if ($wait -eq $WAIT_TIMEOUT) { & $fail "process did not exit ${GracefulTimeoutSec}s after SIGTERM (Ctrl+Break) — graceful shutdown is broken/hanging" 1 }
        [LW.Smoke]::GetExitCodeProcess($pi.hProcess, [ref]$ec) | Out-Null
        if ($ec -ne 0) { & $fail "process exited with code $ec after SIGTERM, expected 0" 1 }
        Write-Host "    OK  graceful shutdown, exit code 0" -ForegroundColor Green
    }
} finally {
    # Never leak a server process.
    [LW.Smoke]::GetExitCodeProcess($pi.hProcess, [ref]$ec) | Out-Null
    if ($ec -eq 259) {
        if (-not [LW.Smoke]::GenerateConsoleCtrlEvent($CTRL_BREAK_EVENT, $pid2)) {
            Stop-Process -Id $pid2 -Force -ErrorAction SilentlyContinue
        }
        [LW.Smoke]::WaitForSingleObject($pi.hProcess, 5000) | Out-Null
    }
    [LW.Smoke]::CloseHandle($pi.hThread) | Out-Null
    [LW.Smoke]::CloseHandle($pi.hProcess) | Out-Null
    foreach ($d in @($dataDir, $pluginsDir)) { if (Test-Path $d) { Remove-Item -Recurse -Force $d -ErrorAction SilentlyContinue } }
}

Write-Host "`nBOOT SMOKE PASSED: $BinPath" -ForegroundColor Green
exit 0
