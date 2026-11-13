param(
    [string]$RepoRoot = ""
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($RepoRoot)) {
    $RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
}

function Invoke-InstallerWithTimeout {
    param(
        [string]$FilePath,
        [string[]]$Arguments,
        [string]$LogPath,
        [int]$TimeoutMs = 90000
    )
    Remove-Item -Force $LogPath -ErrorAction SilentlyContinue
    $fullArgs = $Arguments + @("/LOG=`"$LogPath`"")
    $proc = Start-Process -FilePath $FilePath -ArgumentList $fullArgs -PassThru
    if (-not $proc.WaitForExit($TimeoutMs)) {
        try { $proc.Kill() } catch {}
        $tail = if (Test-Path $LogPath) { (Get-Content $LogPath -Tail 30 | Out-String) } else { "(no log)" }
        throw "Process '$FilePath' timed out after $($TimeoutMs)ms. Log tail:`n$tail"
    }
    if ($proc.ExitCode -ne 0) {
        $tail = if (Test-Path $LogPath) { (Get-Content $LogPath -Tail 30 | Out-String) } else { "(no log)" }
        throw "Process '$FilePath' exited with code $($proc.ExitCode). Log tail:`n$tail"
    }
}

$SetupArm64 = Join-Path $RepoRoot "windows\dist\ClashFX-Setup-arm64.exe"
$SetupAmd64 = Join-Path $RepoRoot "windows\dist\ClashFX-Setup-amd64.exe"
if (-not (Test-Path $SetupArm64)) { throw "Missing $SetupArm64" }
if (-not (Test-Path $SetupAmd64)) { throw "Missing $SetupAmd64" }

$LocalSetup = "C:\Temp\ClashFX-Setup-arm64.exe"
New-Item -ItemType Directory -Force -Path "C:\Temp" | Out-Null
Copy-Item -Force $SetupArm64 $LocalSetup

$InstallDir = "C:\Program Files\ClashFX"
$RegInternetSettings = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings"
$OrigReg = Get-ItemProperty -Path $RegInternetSettings
$OrigProxyEnable = [int]$OrigReg.ProxyEnable
$OrigProxyServer = $OrigReg.ProxyServer

try {
    Write-Host "=== [1/4] Silent Install of ClashFX-Setup-arm64.exe ==="
    Invoke-InstallerWithTimeout -FilePath $LocalSetup `
        -Arguments @("/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/DIR=`"$InstallDir`"") `
        -LogPath "C:\Temp\clashfx-verify-install.log"

    foreach ($f in @("clashfx-ui.exe", "clashfx-daemon.exe", "clashfx-service.exe", "mihomo_core.exe", "wintun.dll", "unins000.exe", "resources\dashboard\index.html")) {
        $full = Join-Path $InstallDir $f
        if (-not (Test-Path $full)) {
            throw "Installed file missing: $full"
        }
    }

    $svc = Get-Service -Name "ClashFXService" -ErrorAction Stop
    if ($svc.Status -ne "Running") {
        throw "Expected ClashFXService to be Running after install, got $($svc.Status)"
    }
    Write-Host "  [OK] Files installed in '$InstallDir' and ClashFXService is Running"

    New-PSDrive -Name HKCR -PSProvider Registry -Root HKEY_CLASSES_ROOT -ErrorAction SilentlyContinue | Out-Null
    foreach ($scheme in @("clash", "clashfx")) {
        $cmdVal = (Get-ItemProperty -Path "HKCR:\$scheme\shell\open\command" -ErrorAction Stop)."(default)"
        if ($cmdVal -notlike "*clashfx-ui.exe*") {
            throw "Unexpected protocol registration for ${scheme}:// -> $cmdVal"
        }
    }
    Write-Host "  [OK] URL protocols clash:// and clashfx:// registered in HKCR"

    Write-Host "=== [2/4] Zero-UAC Enhanced TUN Activation from Installed Directory ==="
    $TestData = "C:\Temp\clashfx-installed-userdata"
    if (Test-Path $TestData) { Remove-Item -Recurse -Force $TestData }
    New-Item -ItemType Directory -Force -Path $TestData | Out-Null

    $PipeName = "ClashFXDaemon-InstalledVerify"

    $daemonProc = Start-Process -FilePath (Join-Path $InstallDir "clashfx-daemon.exe") `
        -ArgumentList @("-pipe", $PipeName, "-data-dir", $TestData) `
        -WorkingDirectory $InstallDir -WindowStyle Hidden -PassThru

    function Invoke-DaemonRpc {
        param([string]$Method, [string]$ParamsJson = "{}")
        $pipe = New-Object System.IO.Pipes.NamedPipeClientStream(".", $PipeName, [System.IO.Pipes.PipeDirection]::InOut)
        $pipe.Connect(5000)
        try {
            $writer = New-Object System.IO.StreamWriter($pipe, (New-Object System.Text.UTF8Encoding($false)))
            $writer.AutoFlush = $true
            $reader = New-Object System.IO.StreamReader($pipe, [System.Text.Encoding]::UTF8)
            $writer.WriteLine("{`"id`":1,`"method`":`"$Method`",`"params`":$ParamsJson}")
            return ($reader.ReadLine() | ConvertFrom-Json)
        } finally {
            $pipe.Dispose()
        }
    }

    $ready = $false
    for ($i = 0; $i -lt 25; $i++) {
        Start-Sleep -Milliseconds 500
        try {
            $st = Invoke-DaemonRpc -Method "get_state"
            if ($st.ok -and $st.data.core_running -and $st.data.service_installed) {
                $ready = $true
                break
            }
        } catch {}
    }
    if (-not $ready) {
        throw "Installed clashfx-daemon did not reach ready state"
    }

    # Toggle Enhanced Mode ON with allow_uac=false -> proves zero UAC prompt needed!
    $tunOn = Invoke-DaemonRpc -Method "set_enhanced_mode" -ParamsJson '{"enabled":true,"allow_uac":false}'
    if (-not $tunOn.ok -or -not $tunOn.data.core_via_service) {
        throw "Zero-UAC Enhanced Mode failed: $($tunOn | ConvertTo-Json -Compress)"
    }
    Write-Host "  [OK] Zero-UAC Enhanced Mode (TUN via ClashFXService) verified!"

    Write-Host "=== [3/4] Smooth In-Place Upgrade While Running ==="
    Invoke-InstallerWithTimeout -FilePath $LocalSetup `
        -Arguments @("/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/DIR=`"$InstallDir`"") `
        -LogPath "C:\Temp\clashfx-verify-upgrade.log"

    $svcAfterUpgrade = Get-Service -Name "ClashFXService" -ErrorAction Stop
    if ($svcAfterUpgrade.Status -ne "Running") {
        throw "Expected ClashFXService Running after in-place upgrade, got $($svcAfterUpgrade.Status)"
    }
    Write-Host "  [OK] Smooth in-place upgrade succeeded while active; ClashFXService Running"

    Write-Host "=== [4/4] Silent Uninstall & P0-1 Proxy/Kill-Switch Cleanup Check ==="
    # Simulate AI Lock kill-switch (127.0.0.1:9) active right before uninstall to verify P0-1!
    Set-ItemProperty -Path $RegInternetSettings -Name ProxyEnable -Value 1 -Type DWord
    Set-ItemProperty -Path $RegInternetSettings -Name ProxyServer -Value "127.0.0.1:9" -Type String

    $uninsExe = Join-Path $InstallDir "unins000.exe"
    Invoke-InstallerWithTimeout -FilePath $uninsExe `
        -Arguments @("/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART") `
        -LogPath "C:\Temp\clashfx-verify-uninstall.log"

    Start-Sleep -Seconds 2
    $afterUninsReg = Get-ItemProperty -Path $RegInternetSettings
    if ([int]$afterUninsReg.ProxyEnable -ne 0) {
        throw "P0-1 regression: ProxyEnable remained $($afterUninsReg.ProxyEnable) ($($afterUninsReg.ProxyServer)) after uninstall!"
    }
    Write-Host "  [OK] P0-1 verified: WinINet loopback/kill-switch proxy cleaned to ProxyEnable=0 after uninstall"

    $svcGone = Get-Service -Name "ClashFXService" -ErrorAction SilentlyContinue
    if ($null -ne $svcGone) {
        throw "ClashFXService still registered after uninstall!"
    }
    foreach ($scheme in @("clash", "clashfx")) {
        if (Test-Path "HKCR:\$scheme") {
            throw "Registry key HKCR:\$scheme still present after uninstall!"
        }
    }
    $leftProcs = @(Get-Process clashfx-ui, clashfx-daemon, clashfx-service, mihomo_core -ErrorAction SilentlyContinue)
    if ($leftProcs.Count -gt 0) {
        throw "Leftover processes after uninstall: $($leftProcs.Name -join ', ')"
    }

    Write-Host "=== [ALL PASSED] Installer install + zero-UAC TUN + in-place upgrade + P0-1 clean uninstall verified! ==="
} finally {
    Set-ItemProperty -Path $RegInternetSettings -Name ProxyEnable -Value $OrigProxyEnable -Type DWord -ErrorAction SilentlyContinue
    if ($null -ne $OrigProxyServer) {
        Set-ItemProperty -Path $RegInternetSettings -Name ProxyServer -Value $OrigProxyServer -Type String -ErrorAction SilentlyContinue
    }
    Remove-Item -Force $LocalSetup -ErrorAction SilentlyContinue
    if ($TestData -and (Test-Path $TestData)) {
        Remove-Item -Recurse -Force $TestData -ErrorAction SilentlyContinue
    }
}
