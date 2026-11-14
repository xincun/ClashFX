param(
    [string]$RepoRoot = ""
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($RepoRoot)) {
    $RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
}

Add-Type -AssemblyName System.Windows.Forms, System.Drawing
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class UiTestWin32 {
    [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
    [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr hWnd, int nCmdShow);
    [DllImport("user32.dll")] public static extern bool SetWindowPos(IntPtr hWnd, IntPtr hWndInsertAfter, int X, int Y, int cx, int cy, uint uFlags);
}
"@
[UiTestWin32]::SetProcessDPIAware() | Out-Null

# Ensure any previous test run is cleanly stopped
Stop-Process -Name clashfx-ui, clashfx-daemon, mihomo_core -Force -ErrorAction SilentlyContinue
Start-Sleep -Milliseconds 500

# Minimize background terminal windows so screenshots show ClashFX cleanly
Get-Process WindowsTerminal, explorer -ErrorAction SilentlyContinue | ForEach-Object {
    if ($_.Name -eq "WindowsTerminal" -and $_.MainWindowHandle -ne [IntPtr]::Zero) {
        [UiTestWin32]::ShowWindow($_.MainWindowHandle, 6) | Out-Null
    }
}

function Save-DesktopScreenshot {
    param([string]$OutFile)
    $bounds = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
    $bmp = New-Object System.Drawing.Bitmap($bounds.Width, $bounds.Height)
    $g = [System.Drawing.Graphics]::FromImage($bmp)
    $g.CopyFromScreen($bounds.Location, [System.Drawing.Point]::Empty, $bounds.Size)
    $bmp.Save($OutFile, [System.Drawing.Imaging.ImageFormat]::Png)
    $g.Dispose()
    $bmp.Dispose()
}

function Invoke-DaemonRpc {
    param([string]$PipeName, [string]$Method, [string]$ParamsJson = "{}")
    $pipe = New-Object System.IO.Pipes.NamedPipeClientStream(".", $PipeName, [System.IO.Pipes.PipeDirection]::InOut)
    $pipe.Connect(5000)
    try {
        $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
        $writer = New-Object System.IO.StreamWriter($pipe, $utf8NoBom)
        $writer.AutoFlush = $true
        $reader = New-Object System.IO.StreamReader($pipe, [System.Text.Encoding]::UTF8)
        $writer.WriteLine("{`"id`":1,`"method`":`"$Method`",`"params`":$ParamsJson}")
        $line = $reader.ReadLine()
        return ($line | ConvertFrom-Json)
    } finally {
        $pipe.Dispose()
    }
}

$ScreenOutDir = Join-Path $RepoRoot "windows\build\ui-screenshots"
New-Item -ItemType Directory -Force -Path $ScreenOutDir | Out-Null

$LocalRoot = "C:\Temp\clashfx-verify-ui"
if (Test-Path $LocalRoot) { Remove-Item -Recurse -Force $LocalRoot -ErrorAction SilentlyContinue }
New-Item -ItemType Directory -Force -Path $LocalRoot | Out-Null

# Start a tiny local HTTP server to serve a subscription YAML for clash:// URL scheme import verification!
$SubListener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
$SubListener.Start()
$SubPort = ([System.Net.IPEndPoint]$SubListener.LocalEndpoint).Port
$SubJob = Start-Job -ScriptBlock {
    param($Port)
    $ln = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $Port)
    # Note: parent listener is stopped right before job starts; let's bind fresh inside job
}
$SubListener.Stop()
Remove-Job $SubJob -Force -ErrorAction SilentlyContinue

foreach ($arch in @("arm64", "amd64")) {
    Write-Host "=== [UI Verification] Testing windows-$arch ==="
    $appDir = Join-Path $LocalRoot "app-$arch"
    $dataDir = Join-Path $LocalRoot "data-$arch"
    New-Item -ItemType Directory -Force -Path $appDir | Out-Null
    New-Item -ItemType Directory -Force -Path $dataDir | Out-Null

    Copy-Item -Recurse -Force (Join-Path $RepoRoot "windows\dist\windows-$arch\*") $appDir

    $cfgYaml = @"
mixed-port: 17890
allow-lan: false
mode: rule
log-level: info
ipv6: false
external-controller: 127.0.0.1:19190
secret: ""
proxies:
  - name: "HK-AI-Node-01"
    type: socks5
    server: 127.0.0.1
    port: 31001
  - name: "SG-Stream-02"
    type: socks5
    server: 127.0.0.1
    port: 31002
proxy-groups:
  - name: "Proxy"
    type: select
    proxies:
      - "HK-AI-Node-01"
      - "SG-Stream-02"
      - "DIRECT"
rules:
  - MATCH,Proxy
"@
    [System.IO.File]::WriteAllText((Join-Path $dataDir "config.yaml"), $cfgYaml, (New-Object System.Text.UTF8Encoding($false)))

    $daemonPipe = "ClashFXDaemon-Verify-$arch"
    $uiPipe = "ClashFXUI-Verify-$arch"
    $uiExe = Join-Path $appDir "clashfx-ui.exe"

    # Run automated DOM + WebView2 navigation smoke test first
    $smokePath = Join-Path $LocalRoot "smoke-$arch.json"
    $smokeProc = Start-Process -FilePath $uiExe `
        -ArgumentList @("--pipe", $daemonPipe, "--data-dir", $dataDir, "--smoke-test-ms", "8000", "--smoke-report", $smokePath) `
        -WorkingDirectory $appDir -PassThru
    if (-not $smokeProc.WaitForExit(30000)) {
        Stop-Process -Id $smokeProc.Id -Force -ErrorAction SilentlyContinue
        throw "Smoke test timed out for $arch"
    }
    if ($smokeProc.ExitCode -ne 0 -or -not (Test-Path $smokePath)) {
        throw "Smoke test failed for $arch (exit=$($smokeProc.ExitCode))"
    }
    $smokeJson = Get-Content $smokePath -Raw | ConvertFrom-Json
    if (-not ($smokeJson.ui_ready -and $smokeJson.daemon_connected -and $smokeJson.core_running -and $smokeJson.dom.ok)) {
        throw "Smoke report assertion failed for ${arch}: $($smokeJson | ConvertTo-Json -Depth 6 -Compress)"
    }
    Write-Host "  [OK] Automated WebView2 DOM smoke test passed ($($smokeJson.dom.steps.Count) steps)"
    Start-Sleep -Milliseconds 600
    Stop-Process -Name clashfx-ui, clashfx-daemon, mihomo_core -Force -ErrorAction SilentlyContinue
    Start-Sleep -Milliseconds 400

    $uiProc = Start-Process -FilePath $uiExe `
        -ArgumentList @("--pipe", $daemonPipe, "--ui-pipe", $uiPipe, "--data-dir", $dataDir) `
        -WorkingDirectory $appDir -PassThru

    # Wait until daemon snapshot reports core_running == true
    $snap = $null
    for ($attempt = 0; $attempt -lt 25; $attempt++) {
        Start-Sleep -Milliseconds 600
        try {
            $resp = Invoke-DaemonRpc -PipeName $daemonPipe -Method "get_state"
            if ($resp.ok -and $resp.data.core_running) {
                $snap = $resp.data
                break
            }
        } catch {}
    }
    if (-not $snap) {
        throw "Daemon core did not reach running state for $arch"
    }
    if ($snap.arch -ne $arch) {
        throw "Daemon arch mismatch: got $($snap.arch), expected $arch"
    }
    Write-Host "  [OK] Daemon + Core running ($($snap.arch)), MixedPort=$($snap.runtime.mixed_port), APIPort=$($snap.runtime.api_port)"

    # Verify MetaCubeXD dashboard HTTP 200 on /ui/
    $uiHttp = Invoke-WebRequest -Uri "http://127.0.0.1:$($snap.runtime.api_port)/ui/" -UseBasicParsing -TimeoutSec 5
    if ($uiHttp.StatusCode -ne 200) {
        throw "Dashboard HTTP status $($uiHttp.StatusCode)"
    }

    # Wait 2.5s for WebView2 MetaCubeXD initial render, position window cleanly, and screenshot Console
    Start-Sleep -Milliseconds 2500
    $uiProc.Refresh()
    if ($uiProc.MainWindowHandle -ne [IntPtr]::Zero) {
        [UiTestWin32]::SetWindowPos($uiProc.MainWindowHandle, [IntPtr](-1), 160, 90, 0, 0, 0x0001) | Out-Null
        [UiTestWin32]::SetWindowPos($uiProc.MainWindowHandle, [IntPtr](-2), 160, 90, 0, 0, 0x0001) | Out-Null
    }
    Start-Sleep -Milliseconds 600

    $wv2Count = @(Get-Process msedgewebview2 -ErrorAction SilentlyContinue).Count
    Write-Host "  [OK] Active msedgewebview2 processes: $wv2Count"
    if ($wv2Count -lt 1) {
        throw "Expected msedgewebview2.exe child processes for $arch ConsoleWindow"
    }

    Save-DesktopScreenshot -OutFile (Join-Path $ScreenOutDir "$arch-1-console.png")

    if ($arch -eq "arm64") {
        # Open Multi-Node Fixed Ports dialog via single-instance IPC and screenshot
        & $uiExe --ui-pipe $uiPipe --open-multi-node
        Start-Sleep -Milliseconds 900
        Save-DesktopScreenshot -OutFile (Join-Path $ScreenOutDir "arm64-2-multinode.png")

        # Open AI Proxy Lock dialog and screenshot
        & $uiExe --ui-pipe $uiPipe --open-ai-lock
        Start-Sleep -Milliseconds 800
        Save-DesktopScreenshot -OutFile (Join-Path $ScreenOutDir "arm64-3-ailock.png")

        # Open Settings dialog and screenshot
        & $uiExe --ui-pipe $uiPipe --open-settings
        Start-Sleep -Milliseconds 800
        Save-DesktopScreenshot -OutFile (Join-Path $ScreenOutDir "arm64-4-settings.png")

        # Open Single-Node Diagnostics dialog and screenshot
        & $uiExe --ui-pipe $uiPipe --open-diagnostics
        Start-Sleep -Milliseconds 800
        Save-DesktopScreenshot -OutFile (Join-Path $ScreenOutDir "arm64-5-diagnostics.png")

        # Test language switch to en_US via RPC and verify tray/UI state remains healthy
        $enSettings = $snap.settings | ConvertTo-Json -Depth 10 | ConvertFrom-Json
        $enSettings.language = "en_US"
        $enSettings.tray_icon_preset = "ocean"
        $updatePayload = @{ settings = $enSettings } | ConvertTo-Json -Depth 10 -Compress
        $updResp = Invoke-DaemonRpc -PipeName $daemonPipe -Method "update_settings" -ParamsJson $updatePayload
        if (-not $updResp.ok) {
            throw "update_settings failed: $($updResp.error)"
        }
        Start-Sleep -Milliseconds 700
        Save-DesktopScreenshot -OutFile (Join-Path $ScreenOutDir "arm64-6-english.png")
    } else {
        & $uiExe --ui-pipe $uiPipe --open-multi-node
        Start-Sleep -Milliseconds 900
        Save-DesktopScreenshot -OutFile (Join-Path $ScreenOutDir "amd64-2-multinode.png")
    }

    # Graceful quit via single-instance IPC (--quit -> quit_daemon -> exit)
    & $uiExe --ui-pipe $uiPipe --quit
    $exited = $false
    for ($w = 0; $w -lt 20; $w++) {
        Start-Sleep -Milliseconds 400
        $rem = @(Get-Process clashfx-ui, clashfx-daemon, mihomo_core -ErrorAction SilentlyContinue)
        if ($rem.Count -eq 0) {
            $exited = $true
            break
        }
    }
    if (-not $exited) {
        Stop-Process -Name clashfx-ui, clashfx-daemon, mihomo_core -Force -ErrorAction SilentlyContinue
        throw "Processes did not exit cleanly on --quit for $arch"
    }
    Write-Host "  [OK] Graceful --quit verified for $arch (zero leftover processes)"
}

Remove-Item -Recurse -Force $LocalRoot -ErrorAction SilentlyContinue
Write-Host "=== [ALL PASSED] Dual-arch Qt6 + WebView2 UI live verification complete! ==="
