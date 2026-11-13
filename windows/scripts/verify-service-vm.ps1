param(
    [ValidateSet("setup", "client", "teardown")]
    [string]$Phase = "setup",
    [string]$BinRoot = ""
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
$utf8NoBom = New-Object System.Text.UTF8Encoding($false)

if ([string]::IsNullOrWhiteSpace($BinRoot)) {
    $BinRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\build\bin")).Path
}

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
function Invoke-DaemonRpc([string]$PipeName, [string]$Method, [string]$ParamsJson = "{}") {
    $pipe = New-Object System.IO.Pipes.NamedPipeClientStream(".", $PipeName, [System.IO.Pipes.PipeDirection]::InOut)
    $pipe.Connect(5000)
    try {
        $utf8 = New-Object System.Text.UTF8Encoding($false)
        $writer = New-Object System.IO.StreamWriter($pipe, $utf8, 4096, $true)
        $writer.AutoFlush = $true
        $reader = New-Object System.IO.StreamReader($pipe, $utf8, $false, 4096, $true)
        $writer.WriteLine("{`"id`":1,`"method`":`"$Method`",`"params`":$ParamsJson}")
        return ($reader.ReadLine() | ConvertFrom-Json)
    } finally {
        $pipe.Dispose()
    }
}

$daemonProc = $null
try {
    $who = (whoami | Out-String).Trim()
    $groups = (whoami /groups | Out-String)
    $isMedium = $groups -match "S-1-16-8192"
    Log "User=$who Medium-IL=$isMedium"
    if (-not $isMedium) {
        throw "Expected Medium Integrity Level (S-1-16-8192), got: $groups"
    }

    # P0-2 check 1: Untrusted process (powershell.exe) MUST be rejected immediately by VerifyPipePeer!
    $rejected = $false
    $rejectReason = ""
    $pipe = New-Object System.IO.Pipes.NamedPipeClientStream(".", "ClashFXService", [System.IO.Pipes.PipeDirection]::InOut)
    $pipe.Connect(5000)
    try {
        $utf8 = New-Object System.Text.UTF8Encoding($false)
        $reader = New-Object System.IO.StreamReader($pipe, $utf8, $false, 4096, $true)
        $writer = New-Object System.IO.StreamWriter($pipe, $utf8, 4096, $true)
        $writer.AutoFlush = $true
        try {
            $writer.WriteLine('{"action":"start_core","home_dir":"C:\\Temp\\clashfx-p0-2\\home","config_file":"C:\\Temp\\clashfx-p0-2\\home\\config.yaml"}')
        } catch {
            $rejected = $true
            $rejectReason = $_.Exception.Message
        }
        $line = $null
        try { $line = $reader.ReadLine() } catch {}
        if ($line) {
            $resp = $line | ConvertFrom-Json
            if (-not $resp.ok -and $resp.message -match "unauthorized pipe caller") {
                $rejected = $true
                $rejectReason = $resp.message
            }
        }
    } finally {
        $pipe.Dispose()
    }
    if (-not $rejected) {
        throw "Expected powershell.exe caller to be rejected by VerifyPipePeer!"
    }
    Log "P0-2 verified: untrusted pipe caller rejected ($rejectReason)"

    # P0-2 check 2: Trusted clashfx-daemon.exe inside C:\Program Files\ClashFXTest succeeds with zero UAC!
    $daemonPipe = "ClashFXDaemon-P02Verify"
    $daemonProc = Start-Process -FilePath "C:\Program Files\ClashFXTest\clashfx-daemon.exe" `
        -ArgumentList @("-pipe", $daemonPipe, "-data-dir", "C:\Temp\clashfx-p0-2\home") `
        -WorkingDirectory "C:\Program Files\ClashFXTest" -WindowStyle Hidden -PassThru

    $ready = $false
    for ($i = 0; $i -lt 30; $i++) {
        Start-Sleep -Milliseconds 400
        try {
            $st = Invoke-DaemonRpc -PipeName $daemonPipe -Method "get_state"
            if ($st.ok -and $st.data.core_running -and $st.data.service_installed) {
                $ready = $true
                break
            }
        } catch {}
    }
    if (-not $ready) {
        throw "Trusted clashfx-daemon.exe failed to become ready"
    }

    $tunOn = Invoke-DaemonRpc -PipeName $daemonPipe -Method "set_enhanced_mode" -ParamsJson '{"enabled":true,"allow_uac":false}'
    if (-not $tunOn.ok -or -not $tunOn.data.core_via_service) {
        throw "Trusted daemon zero-UAC TUN failed: $($tunOn | ConvertTo-Json -Compress)"
    }
    Log "Trusted daemon TUN UP via service: core_via_service=$($tunOn.data.core_via_service)"

    [void](Invoke-DaemonRpc -PipeName $daemonPipe -Method "quit_daemon" -ParamsJson '{"force_restore_proxy":true}')
    [void]$daemonProc.WaitForExit(10000)
    $daemonProc = $null
    Log "CLIENT_OK"
} catch {
    Log "CLIENT_ERR: $_"
    exit 1
} finally {
    if ($null -ne $daemonProc -and -not $daemonProc.HasExited) {
        try { $daemonProc.Kill() } catch {}
    }
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
