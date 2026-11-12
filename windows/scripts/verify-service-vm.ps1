param(
    [ValidateSet("setup", "client", "teardown")]
    [string]$Phase = "setup",
    [string]$BinRoot = "C:\Mac\Home\Desktop\workspace\github\ClashFX\windows\build\bin"
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)

$installDir = "C:\Program Files\ClashFXTest"
$testDataDir = "C:\Temp\clashfx-p0-2"

if ($Phase -eq "setup") {
    if (Test-Path "$installDir\clashfx-service.exe") {
        & "$installDir\clashfx-service.exe" uninstall 2>$null | Out-Null
    }
    if (Test-Path $installDir) { Remove-Item -Recurse -Force $installDir }
    if (Test-Path $testDataDir) { Remove-Item -Recurse -Force $testDataDir }

    New-Item -ItemType Directory -Path $installDir, "$testDataDir\amd64", "$testDataDir\home" | Out-Null
    icacls $testDataDir /grant "*S-1-1-0:(OI)(CI)F" /T /Q | Out-Null

    Copy-Item "$BinRoot\arm64\*" "$installDir\" -Force
    Copy-Item "$BinRoot\amd64\clashfx-service.exe" "$testDataDir\amd64\" -Force

    $amdVer = (& "$testDataDir\amd64\clashfx-service.exe" version | Out-String).Trim()
    if ($LASTEXITCODE -ne 0 -or $amdVer -notmatch "clashfx-service") {
        throw "amd64 clashfx-service.exe failed smoke check: $amdVer"
    }
    Write-Host "[amd64] binary smoke check: $amdVer"

    & "$installDir\clashfx-service.exe" install
    if ($LASTEXITCODE -ne 0) {
        throw "clashfx-service.exe install failed ($LASTEXITCODE)"
    }
    $svc = Get-Service -Name "ClashFXService"
    if ($svc.Status -ne "Running") {
        throw "ClashFXService is not Running: $($svc.Status)"
    }
    Write-Host "[service] ClashFXService installed & running"

    $tunCfg = @"
mixed-port: 17894
allow-lan: false
external-controller: 127.0.0.1:19094
dns:
  enable: true
  listen: 127.0.0.1:11054
  enhanced-mode: fake-ip
  fake-ip-range: 198.18.0.1/16
  default-nameserver: [223.5.5.5, 1.1.1.1]
  nameserver: [223.5.5.5, 1.1.1.1]
tun:
  enable: true
  device: ClashFXSvcTun
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
    [System.IO.File]::WriteAllText("$testDataDir\home\config.yaml", $tunCfg, $utf8NoBom)

    $innerScript = @'
$ErrorActionPreference = "Stop"
$logFile = "C:\Temp\clashfx-p0-2\client.log"
function Log($msg) {
    Add-Content -Path $logFile -Value $msg -Encoding UTF8
}
function Invoke-ServicePipe([hashtable]$payload) {
    $pipe = New-Object System.IO.Pipes.NamedPipeClientStream(".", "ClashFXService", [System.IO.Pipes.PipeDirection]::InOut)
    $pipe.Connect(5000)
    try {
        $utf8 = New-Object System.Text.UTF8Encoding($false)
        $writer = New-Object System.IO.StreamWriter($pipe, $utf8, 4096, $true)
        $writer.AutoFlush = $true
        $reader = New-Object System.IO.StreamReader($pipe, $utf8, $false, 4096, $true)
        $json = ($payload | ConvertTo-Json -Compress)
        $writer.WriteLine($json)
        $line = $reader.ReadLine()
        return ($line | ConvertFrom-Json)
    } finally {
        $pipe.Dispose()
    }
}

try {
    $who = (whoami | Out-String).Trim()
    $groups = (whoami /groups | Out-String)
    $isMedium = $groups -match "S-1-16-8192"
    Log "User=$who Medium-IL=$isMedium"
    if (-not $isMedium) {
        throw "Expected Medium Integrity Level (S-1-16-8192), got: $groups"
    }

    $bad = Invoke-ServicePipe @{ action = "start_core"; home_dir = "\\evil\unc"; config_file = "C:\Temp\clashfx-p0-2\home\config.yaml" }
    if ($bad.ok) {
        throw "UNC path should have been rejected"
    }
    Log "Security check rejected UNC path: $($bad.message)"

    for ($cycle = 1; $cycle -le 3; $cycle++) {
        $startResp = Invoke-ServicePipe @{
            action = "start_core"
            home_dir = "C:\Temp\clashfx-p0-2\home"
            config_file = "C:\Temp\clashfx-p0-2\home\config.yaml"
        }
        if (-not $startResp.ok -or -not $startResp.status.running) {
            throw "Cycle $cycle start_core failed: $($startResp | ConvertTo-Json -Compress)"
        }
        $adapter = Get-NetAdapter -Name "ClashFXSvcTun" -ErrorAction SilentlyContinue
        if (-not $adapter -or $adapter.Status -ne "Up") {
            throw "Cycle $cycle adapter ClashFXSvcTun not Up"
        }
        $ver = Invoke-RestMethod -Uri "http://127.0.0.1:19094/version" -TimeoutSec 2
        Log "Cycle $cycle UP: pid=$($startResp.status.pid) adapter=$($adapter.Status) version=$($ver.version)"

        $stopResp = Invoke-ServicePipe @{ action = "stop_core" }
        if (-not $stopResp.ok -or $stopResp.status.running) {
            throw "Cycle $cycle stop_core failed: $($stopResp | ConvertTo-Json -Compress)"
        }
        Start-Sleep -Milliseconds 400
        $left = Get-NetAdapter -Name "ClashFXSvcTun" -ErrorAction SilentlyContinue
        if ($left) {
            throw "Cycle $cycle leftover adapter $($left.Name)"
        }
        Log "Cycle $cycle STOPPED cleanly (zero adapter residue)"
    }
    Log "CLIENT_OK"
} catch {
    Log "CLIENT_ERR: $_"
    exit 1
}
'@
    [System.IO.File]::WriteAllText("$testDataDir\inner-client.ps1", $innerScript, $utf8NoBom)
    exit 0
}

if ($Phase -eq "client") {
    & runas.exe /trustlevel:0x20000 "powershell.exe -NonInteractive -NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File C:\Temp\clashfx-p0-2\inner-client.ps1" | Out-Null
    for ($i = 0; $i -lt 120; $i++) {
        if (Test-Path "$testDataDir\client.log") {
            $content = Get-Content "$testDataDir\client.log" -Raw -ErrorAction SilentlyContinue
            if ($content -match "CLIENT_OK" -or $content -match "CLIENT_ERR:") {
                break
            }
        }
        Start-Sleep -Milliseconds 250
    }
    $logOutput = if (Test-Path "$testDataDir\client.log") { (Get-Content "$testDataDir\client.log" -Raw).Trim() } else { "(no client log)" }
    Write-Host $logOutput
    if ($logOutput -notmatch "CLIENT_OK") {
        throw "Medium-IL client verification failed"
    }
    exit 0
}

if ($Phase -eq "teardown") {
    if (Test-Path "$installDir\clashfx-service.exe") {
        & "$installDir\clashfx-service.exe" uninstall
        if ($LASTEXITCODE -ne 0) {
            throw "clashfx-service.exe uninstall failed ($LASTEXITCODE)"
        }
    }
    if (Get-Service -Name "ClashFXService" -ErrorAction SilentlyContinue) {
        throw "ClashFXService still registered after uninstall"
    }
    Write-Host "[service] ClashFXService uninstalled cleanly"
    if (Test-Path $installDir) { Remove-Item -Recurse -Force $installDir }
    if (Test-Path $testDataDir) { Remove-Item -Recurse -Force $testDataDir }
    Write-Host "ALL P0-2 VM CHECKS PASSED"
}
