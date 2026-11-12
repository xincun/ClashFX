param(
    [string]$BinRoot = "C:\Mac\Home\Desktop\workspace\github\ClashFX\windows\build\bin"
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

Get-Process -Name "mihomo_core" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue

$testRoot = "C:\Temp\clashfx-p0-1"
if (Test-Path $testRoot) { Remove-Item -Recurse -Force $testRoot }
New-Item -ItemType Directory -Path "$testRoot\arm64", "$testRoot\amd64", "$testRoot\home-arm64", "$testRoot\home-amd64", "$testRoot\home-tun" | Out-Null

Copy-Item "$BinRoot\arm64\*" "$testRoot\arm64\" -Force
Copy-Item "$BinRoot\amd64\*" "$testRoot\amd64\" -Force
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)

foreach ($arch in @("arm64", "amd64")) {
    $exe = "$testRoot\$arch\mihomo_core.exe"
    $feat = (& $exe --features | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $feat -notmatch "with_gvisor") {
        throw "$arch --features failed ($LASTEXITCODE): $feat"
    }
    Write-Host "[$arch] features: $feat"

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
    if ($LASTEXITCODE -ne 0 -or $val -notmatch "configuration is valid") {
        throw "$arch -t failed ($LASTEXITCODE): $val"
    }
    Write-Host "[$arch] -t: $val"

    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $exe
    $psi.WorkingDirectory = "$testRoot\$arch"
    $psi.Arguments = "-d `"$homeDir`" -f `"$homeDir\config.yaml`" -watch-stdin"
    $psi.RedirectStandardInput = $true
    $psi.RedirectStandardOutput = $false
    $psi.RedirectStandardError = $false
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

    $proc.StandardInput.Close()
    if (-not $proc.WaitForExit(5000)) {
        $proc.Kill()
        throw "$arch did not exit on stdin EOF"
    }
    if ($proc.ExitCode -ne 0) {
        throw "$arch exit code $($proc.ExitCode)"
    }
    Write-Host "[$arch] graceful shutdown exit=$($proc.ExitCode)"
}

# Elevated ARM64 Wintun TUN startup + adapter creation + graceful teardown
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
$psi.Arguments = "-d `"$testRoot\home-tun`" -f `"$testRoot\home-tun\config.yaml`" -watch-stdin"
$psi.RedirectStandardInput = $true
$psi.RedirectStandardOutput = $false
$psi.RedirectStandardError = $false
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

$tunProc.StandardInput.Close()
if (-not $tunProc.WaitForExit(8000)) {
    $tunProc.Kill()
    throw "TUN process did not exit cleanly on stdin EOF"
}
Write-Host "[arm64-tun] graceful shutdown exit=$($tunProc.ExitCode)"
Start-Sleep -Milliseconds 500
$leftover = Get-NetAdapter -Name "ClashFXTunTest" -ErrorAction SilentlyContinue
if ($leftover) {
    throw "Leftover Wintun adapter after shutdown: $($leftover.Name)"
}
Write-Host "[arm64-tun] Wintun adapter cleanly removed (zero residue)"
Remove-Item -Recurse -Force $testRoot
Write-Host "ALL P0-1 VM CHECKS PASSED"
