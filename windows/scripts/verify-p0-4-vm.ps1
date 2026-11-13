param(
    [string]$TestBinRoot = ""
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)

if ([string]::IsNullOrWhiteSpace($TestBinRoot)) {
    $TestBinRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\build\tests")).Path
}

$workRoot = "C:\Temp\clashfx-p0-4"
if (Test-Path $workRoot) {
    Remove-Item -Recurse -Force $workRoot
}
New-Item -ItemType Directory -Path "$workRoot\arm64", "$workRoot\amd64", "$workRoot\crash-app", "$workRoot\tmp" | Out-Null

Copy-Item "$TestBinRoot\arm64\sysproxy.test.exe" "$workRoot\arm64\sysproxy.test.exe" -Force
Copy-Item "$TestBinRoot\amd64\sysproxy.test.exe" "$workRoot\amd64\sysproxy.test.exe" -Force

$innerScript = @'
$ErrorActionPreference = "Stop"
$workRoot = "C:\Temp\clashfx-p0-4"
$logFile = "$workRoot\result.log"
$regPath = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings"

function Log($msg) {
    Add-Content -Path $logFile -Value $msg -Encoding UTF8
}

$origProps = Get-ItemProperty -Path $regPath
$origEnable = [int]$origProps.ProxyEnable
$origServer = $origProps.ProxyServer

try {
    $env:GOTMPDIR = "$workRoot\tmp"
    $env:TEMP = "$workRoot\tmp"
    $env:TMP = "$workRoot\tmp"

    $groups = (whoami /groups | Out-String)
    if ($groups -notmatch "S-1-16-8192") {
        throw "Expected Medium Integrity Level (S-1-16-8192), got: $groups"
    }
    Log "[il] Confirmed Medium Integrity Level (S-1-16-8192)"

    foreach ($arch in @("arm64", "amd64")) {
        $out = (& "$workRoot\$arch\sysproxy.test.exe" "-test.v" | Out-String).Trim()
        if ($LASTEXITCODE -ne 0) {
            throw "[$arch] sysproxy.test.exe failed: $out"
        }
        Log "[$arch] unit+registry suite PASS"
    }

    # External multi-process crash simulation:
    # 1. Baseline: ensure ProxyEnable = 0
    Set-ItemProperty -Path $regPath -Name ProxyEnable -Value 0 -Type DWord

    # 2. Process 1 enables system proxy and exits abruptly without Restore()
    $env:CLASHFX_SYSPROXY_HELPER_DIR = "$workRoot\crash-app"
    $env:CLASHFX_SYSPROXY_HELPER_ACTION = "enable"
    & "$workRoot\arm64\sysproxy.test.exe" | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Crash helper enable failed"
    }

    $afterEnable = Get-ItemProperty -Path $regPath
    if ([int]$afterEnable.ProxyEnable -ne 1 -or $afterEnable.ProxyServer -ne "127.0.0.1:17899") {
        throw "HKCU not updated after enable: ProxyEnable=$($afterEnable.ProxyEnable) ProxyServer=$($afterEnable.ProxyServer)"
    }
    $sysProxy = [System.Net.WebRequest]::GetSystemWebProxy()
    $extUri = $sysProxy.GetProxy([Uri]"https://example.com/")
    $localBypassed = $sysProxy.IsBypassed([Uri]"http://intranet-host/")
    if ($extUri.Authority -ne "127.0.0.1:17899" -or -not $localBypassed) {
        throw "WinINet GetSystemWebProxy mismatch: ext=$($extUri.Authority) localBypassed=$localBypassed"
    }
    Log "[wininet] Active proxy verified: https://example.com -> $($extUri.Authority), <local> bypassed=$localBypassed"

    # 3. Process 2 launches after unclean exit with wantsSystemProxy=false -> self-heals HKCU!
    $env:CLASHFX_SYSPROXY_HELPER_ACTION = "recover"
    & "$workRoot\amd64\sysproxy.test.exe" | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Crash helper recover failed"
    }

    $afterRecover = Get-ItemProperty -Path $regPath
    if ([int]$afterRecover.ProxyEnable -ne 0) {
        throw "Expected ProxyEnable=0 after crash recovery, got $($afterRecover.ProxyEnable)"
    }
    if (Test-Path "$workRoot\crash-app\sysproxy-snapshot.json") {
        throw "sysproxy-snapshot.json was not cleaned up after crash recovery"
    }
    Log "[self-heal] Unclean exit recovered cleanly by fresh amd64 process (ProxyEnable=0)"
    Log "P0_4_OK"
} catch {
    Log "P0_4_ERR: $_"
    exit 1
} finally {
    Set-ItemProperty -Path $regPath -Name ProxyEnable -Value $origEnable -Type DWord -ErrorAction SilentlyContinue
    if ($null -ne $origServer) {
        Set-ItemProperty -Path $regPath -Name ProxyServer -Value $origServer -Type String -ErrorAction SilentlyContinue
    }
}
'@

[System.IO.File]::WriteAllText("$workRoot\inner.ps1", $innerScript, $utf8NoBom)

try {
    & runas.exe /trustlevel:0x20000 "powershell.exe -NonInteractive -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File C:\Temp\clashfx-p0-4\inner.ps1" | Out-Null
    for ($i = 0; $i -lt 80; $i++) {
        if (Test-Path "$workRoot\result.log") {
            $raw = Get-Content "$workRoot\result.log" -Raw -ErrorAction SilentlyContinue
            if ($raw -match "P0_4_OK" -or $raw -match "P0_4_ERR:") {
                break
            }
        }
        Start-Sleep -Milliseconds 200
    }
    $output = if (Test-Path "$workRoot\result.log") { (Get-Content "$workRoot\result.log" -Raw).Trim() } else { "(no result.log)" }
    Write-Host $output
    if ($output -notmatch "P0_4_OK") {
        throw "P0-4 Medium-IL verification failed"
    }
    Write-Host "ALL P0-4 DUAL-ARCH VM CHECKS PASSED"
} finally {
    if (Test-Path $workRoot) {
        Remove-Item -Recurse -Force $workRoot -ErrorAction SilentlyContinue
    }
}
