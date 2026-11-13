param(
    [string]$BinRoot = ""
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

if ([string]::IsNullOrWhiteSpace($BinRoot)) {
    $BinRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\build\bin")).Path
}

Get-Process -Name "mihomo_core" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue

$testRoot = "C:\Temp\clashfx-p0-1"
if (Test-Path $testRoot) { Remove-Item -Recurse -Force $testRoot }
New-Item -ItemType Directory -Path "$testRoot\arm64", "$testRoot\amd64", "$testRoot\home-arm64", "$testRoot\home-amd64", "$testRoot\home-tun" | Out-Null

Copy-Item "$BinRoot\arm64\*" "$testRoot\arm64\" -Force
Copy-Item "$BinRoot\amd64\*" "$testRoot\amd64\" -Force
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)

foreach ($arch in @("arm64", "amd64")) {
    $exe = "$testRoot\$arch\mihomo_core.exe"
    $verOut = (& $exe -v | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $verOut -notmatch "with_gvisor") {
        throw "$arch -v failed ($LASTEXITCODE): $verOut"
    }
    Write-Host "[$arch] -v: $verOut"

    $homeDir = "$testRoot\home-$arch"
    $port = if ($arch -eq "arm64") { 17891 } else { 17892 }
    $apiPort = if ($arch -eq "arm64") { 19091 } else { 19092 }
    $cfgText = @"
mixed-port: $port
allow-lan: false
external-controller: 127.0.0.1:$apiPort
dns:
  enable: false
tun:
  enable: false
rules:
  - MATCH,DIRECT
"@
    [System.IO.File]::WriteAllText("$homeDir\config.yaml", $cfgText, $utf8NoBom)

    $val = (& $exe -t -d $homeDir -f "$homeDir\config.yaml" 2>&1 | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $val -notmatch "test is successful") {
        throw "$arch -t failed ($LASTEXITCODE): $val"
    }
    Write-Host "[$arch] -t: $val"

    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $exe
    $psi.WorkingDirectory = "$testRoot\$arch"
    $psi.Arguments = "-d `"$homeDir`" -f `"$homeDir\config.yaml`""
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $proc = [System.Diagnostics.Process]::Start($psi)

    $started = $false
    for ($i = 0; $i -lt 60; $i++) {
        try {
            $ver = Invoke-RestMethod -Uri "http://127.0.0.1:$apiPort/version" -TimeoutSec 1
            if ($ver.version) { $started = $true; break }
        } catch {}
        if ($proc.HasExited) {
            throw "$arch process exited early with code $($proc.ExitCode)"
        }
        Start-Sleep -Milliseconds 150
    }
    if (-not $started) {
        $proc.Kill()
        throw "$arch normal proxy failed to become ready"
    }
    Write-Host "[$arch] REST version ready: $($ver | ConvertTo-Json -Compress)"

    $proc.Kill()
    [void]$proc.WaitForExit(5000)
    Write-Host "[$arch] clean stop verified"
}

# Elevated ARM64 Wintun TUN startup + adapter creation + clean teardown
$tunCfg = @"
mixed-port: 17893
allow-lan: false
external-controller: 127.0.0.1:19093
dns:
  enable: true
  listen: 127.0.0.1:11053
  enhanced-mode: fake-ip
  fake-ip-range: 198.18.0.1/16
  default-nameserver: [223.5.5.5, 1.1.1.1]
  nameserver: [223.5.5.5, 1.1.1.1]
tun:
  enable: true
  device: ClashFXTunTest
  stack: mixed
  auto-route: true
  auto-detect-interface: true
  dns-hijack:
    - any:53
    - tcp://any:53
  mtu: 1500
rules:
  - PROCESS-NAME,mihomo_core.exe,DIRECT
  - MATCH,DIRECT
"@
[System.IO.File]::WriteAllText("$testRoot\home-tun\config.yaml", $tunCfg, $utf8NoBom)

$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = "$testRoot\arm64\mihomo_core.exe"
$psi.WorkingDirectory = "$testRoot\arm64"
$psi.Arguments = "-d `"$testRoot\home-tun`" -f `"$testRoot\home-tun\config.yaml`""
$psi.UseShellExecute = $false
$psi.CreateNoWindow = $true
$tunProc = [System.Diagnostics.Process]::Start($psi)

$tunAdapterUp = $false
for ($i = 0; $i -lt 80; $i++) {
    $adapter = Get-NetAdapter -Name "ClashFXTunTest" -ErrorAction SilentlyContinue
    if ($adapter -and $adapter.Status -eq "Up") {
        $tunAdapterUp = $true
        Write-Host "[arm64-tun] Wintun adapter UP: Name=$($adapter.Name) Desc=$($adapter.InterfaceDescription) Status=$($adapter.Status)"
        break
    }
    if ($tunProc.HasExited) {
        throw "TUN process exited early ($($tunProc.ExitCode))"
    }
    Start-Sleep -Milliseconds 150
}
if (-not $tunAdapterUp) {
    $tunProc.Kill()
    throw "Wintun adapter ClashFXTunTest did not come UP"
}

$tunProc.Kill()
[void]$tunProc.WaitForExit(8000)
Start-Sleep -Milliseconds 600
$leftover = Get-NetAdapter -Name "ClashFXTunTest" -ErrorAction SilentlyContinue
if ($leftover) {
    throw "Leftover Wintun adapter after shutdown: $($leftover.Name)"
}
Write-Host "[arm64-tun] Wintun adapter cleanly removed (zero residue)"
Remove-Item -Recurse -Force $testRoot
Write-Host "ALL P0-1 STOCK MIHOMO VM CHECKS PASSED"
