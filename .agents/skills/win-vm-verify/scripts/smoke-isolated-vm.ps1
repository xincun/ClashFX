param(
    [ValidateSet("arm64", "amd64")][string]$Arch = "arm64",
    [string]$RepoRoot = "C:\Mac\Home\Desktop\workspace\github\ClashFX",
    [int]$SmokeMs = 6000,
    [int]$TimeoutMs = 30000
)

$ErrorActionPreference = "Stop"
$env:CLASHFX_DISABLE_OS_SYSPROXY = "1"
$env:CLASHFX_DISABLE_LAUNCH_AGENT = "1"
$env:CLASHFX_DISABLE_PRIVILEGED_SERVICE = "1"

$Root = "C:\Temp\clashfx-vm-smoke"
$AppDir = Join-Path $Root "app"
$DataDir = Join-Path $Root "data"
$SmokePath = Join-Path $Root "smoke.json"
$SrcDist = Join-Path $RepoRoot ("windows\dist\windows-" + $Arch)

function Stop-ScopedProcesses {
    param([string]$Prefix)
    Get-Process -ErrorAction SilentlyContinue | Where-Object {
        $_.Path -and $_.Path.StartsWith($Prefix, [System.StringComparison]::OrdinalIgnoreCase)
    } | Stop-Process -Force -ErrorAction SilentlyContinue
}

Stop-ScopedProcesses $Root
if (Test-Path $Root) { Remove-Item -Recurse -Force $Root -ErrorAction SilentlyContinue }
New-Item -ItemType Directory -Force -Path $AppDir, $DataDir | Out-Null
Copy-Item -Recurse -Force (Join-Path $SrcDist "*") $AppDir

$cfgYaml = @"
mixed-port: 17892
allow-lan: false
mode: rule
log-level: info
ipv6: false
external-controller: 127.0.0.1:19192
secret: ""
proxies:
  - name: "HK-Smoke-01"
    type: socks5
    server: 127.0.0.1
    port: 31001
proxy-groups:
  - name: "Proxy"
    type: select
    proxies:
      - "HK-Smoke-01"
      - "DIRECT"
rules:
  - MATCH,Proxy
"@
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)
[System.IO.File]::WriteAllText((Join-Path $DataDir "VerifySub.yaml"), $cfgYaml, $utf8NoBom)
[System.IO.File]::WriteAllText((Join-Path $DataDir "subscriptions.json"), '[{"name":"VerifySub","url":"https://example.invalid/sub.yaml"}]', $utf8NoBom)
[System.IO.File]::WriteAllText((Join-Path $DataDir "settings.json"), '{"selected_profile":"VerifySub","api_port":19192,"port_override":17892}', $utf8NoBom)

$uiExe = Join-Path $AppDir "clashfx-ui.exe"
$proc = $null
try {
    $proc = Start-Process -FilePath $uiExe `
        -ArgumentList @("--pipe", "ClashFXDaemon-Smoke-$Arch", "--ui-pipe", "ClashFXUI-Smoke-$Arch", "--data-dir", $DataDir, "--smoke-test-ms", "$SmokeMs", "--smoke-report", $SmokePath) `
        -WorkingDirectory $AppDir -PassThru

    if (-not $proc.WaitForExit($TimeoutMs)) {
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
        throw "Smoke test timed out after ${TimeoutMs}ms for windows-$Arch"
    }
    if ($proc.ExitCode -ne 0 -or -not (Test-Path $SmokePath)) {
        throw "Smoke test failed for windows-$Arch (exit=$($proc.ExitCode))"
    }

    $smoke = Get-Content $SmokePath -Raw | ConvertFrom-Json
    if (-not ($smoke.ui_ready -and $smoke.daemon_connected -and $smoke.core_running -and $smoke.dom.ok)) {
        throw "Smoke assertion failed for windows-${Arch}: $($smoke | ConvertTo-Json -Depth 6 -Compress)"
    }
    Write-Host "[OK] windows-$Arch isolated VM smoke passed ($($smoke.dom.steps.Count) DOM steps)"
}
finally {
    if ($proc -and -not $proc.HasExited) {
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
    }
    Stop-ScopedProcesses $Root
    Start-Sleep -Milliseconds 250
    if (Test-Path $Root) { Remove-Item -Recurse -Force $Root -ErrorAction SilentlyContinue }
}
