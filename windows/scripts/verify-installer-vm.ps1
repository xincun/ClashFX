param(
    [string]$RepoRoot = "",
    [string]$LocalSetupPath = ""
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($RepoRoot) -and [string]::IsNullOrWhiteSpace($LocalSetupPath)) {
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

New-Item -ItemType Directory -Force -Path "C:\Temp" | Out-Null
$LocalSetup = "C:\Temp\clashfx-verify-setup-work.exe"
if (-not [string]::IsNullOrWhiteSpace($LocalSetupPath)) {
    if (-not (Test-Path $LocalSetupPath)) { throw "Missing LocalSetupPath: $LocalSetupPath" }
    Copy-Item -Force $LocalSetupPath $LocalSetup
} else {
    if ([string]::IsNullOrWhiteSpace($RepoRoot)) {
        $RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
    }
    $SetupArm64 = Join-Path $RepoRoot "windows\dist\ClashFX-Setup-arm64.exe"
    $SetupAmd64 = Join-Path $RepoRoot "windows\dist\ClashFX-Setup-amd64.exe"
    if (-not (Test-Path $SetupArm64)) { throw "Missing $SetupArm64" }
    if (-not (Test-Path $SetupAmd64)) { throw "Missing $SetupAmd64" }
    Copy-Item -Force $SetupArm64 $LocalSetup
}

$InstallDir = "C:\Program Files\ClashFX"
New-PSDrive -Name HKU -PSProvider Registry -Root HKEY_USERS -ErrorAction SilentlyContinue | Out-Null
$RegInternetSettings = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings"
try {
    $interactiveUser = (Get-CimInstance Win32_ComputerSystem -ErrorAction SilentlyContinue).UserName
    if ($interactiveUser) {
        $sid = (New-Object System.Security.Principal.NTAccount($interactiveUser)).Translate([System.Security.Principal.SecurityIdentifier]).Value
        $candidateHive = "HKU:\$sid\Software\Microsoft\Windows\CurrentVersion\Internet Settings"
        if (Test-Path $candidateHive) {
            $RegInternetSettings = $candidateHive
        }
    }
} catch {}
$OrigReg = Get-ItemProperty -Path $RegInternetSettings
$OrigProxyEnable = [int]$OrigReg.ProxyEnable
$OrigProxyServer = $OrigReg.ProxyServer

try {
    Write-Host "=== [1/4] Silent Install of ClashFX-Setup-arm64.exe ==="
    Invoke-InstallerWithTimeout -FilePath $LocalSetup `
        -Arguments @("/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/DIR=`"$InstallDir`"") `
        -LogPath "C:\Temp\clashfx-verify-install.log"

    foreach ($f in @("clashfx-ui.exe", "clashfx-daemon.exe", "clashfx-service.exe", "mihomo_core.exe", "wintun.dll", "vcruntime140.dll", "msvcp140.dll", "unins000.exe", "resources\dashboard\index.html")) {
        $full = Join-Path $InstallDir $f
        if (-not (Test-Path $full)) {
            throw "Installed file missing: $full"
        }
    }

    $svc = Get-Service -Name "ClashFXService" -ErrorAction Stop
    if ($svc.Status -ne "Running") {
        throw "Expected ClashFXService to be Running after install, got $($svc.Status)"
    }
    $fwRule = Get-NetFirewallRule -DisplayName "ClashFX - mihomo_core.exe (In)" -ErrorAction Stop
    Write-Host "  [OK] Files (+ MSVC CRT) installed in '$InstallDir', ClashFXService Running, Firewall registered ($($fwRule.DisplayName))"

    New-PSDrive -Name HKCR -PSProvider Registry -Root HKEY_CLASSES_ROOT -ErrorAction SilentlyContinue | Out-Null
    foreach ($scheme in @("clash", "clashfx")) {
        $cmdVal = (Get-ItemProperty -Path "HKCR:\$scheme\shell\open\command" -ErrorAction Stop)."(default)"
        if ($cmdVal -notlike "*clashfx-ui.exe*") {
            throw "Unexpected protocol registration for ${scheme}:// -> $cmdVal"
        }
    }
    Write-Host "  [OK] URL protocols clash:// and clashfx:// registered in HKCR"

    Write-Host "=== [2/4] Zero-Sub Armed Standby & 1-Click Subscription Zero-UAC Activation ==="
    $ProcInetSettings = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings"
    if (-not (Test-Path $ProcInetSettings)) { New-Item -Path $ProcInetSettings -Force | Out-Null }
    $OrigProcReg = Get-ItemProperty -Path $ProcInetSettings -ErrorAction SilentlyContinue
    $OrigProcProxyEnable = if ($null -ne $OrigProcReg -and $null -ne $OrigProcReg.ProxyEnable) { [int]$OrigProcReg.ProxyEnable } else { 0 }
    $OrigProcProxyServer = if ($null -ne $OrigProcReg) { $OrigProcReg.ProxyServer } else { $null }
    Set-ItemProperty -Path $ProcInetSettings -Name ProxyEnable -Value 0 -Type DWord

    $TestData = "C:\Temp\clashfx-installed-userdata"
    if (Test-Path $TestData) { Remove-Item -Recurse -Force $TestData }
    New-Item -ItemType Directory -Force -Path $TestData | Out-Null

    $PipeName = "ClashFXDaemon-InstalledVerify"
    $subJob = $null
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

    try {
        $snap0 = $null
        for ($i = 0; $i -lt 30; $i++) {
            Start-Sleep -Milliseconds 500
            try {
                $st = Invoke-DaemonRpc -Method "get_state"
                if ($st.ok -and $st.data.core_running -and $st.data.service_installed) {
                    $snap0 = $st.data
                    break
                }
            } catch {}
        }
        if (-not $snap0) {
            throw "Installed clashfx-daemon did not reach ready state"
        }

        if (-not $snap0.settings.system_proxy_enabled -or -not $snap0.settings.enhanced_mode -or -not $snap0.settings.bypass_cn_apps -or $snap0.settings.outbound_mode -ne "rule" -or -not $snap0.settings.auto_update_subs) {
            throw "Out-of-the-box defaults not armed in get_state: $($snap0.settings | ConvertTo-Json -Compress)"
        }
        if ($snap0.core_via_service) {
            throw "Expected 0-sub standby with core_via_service=false before first subscription import"
        }
        $procReg0 = Get-ItemProperty -Path $ProcInetSettings -ErrorAction SilentlyContinue
        if ($null -ne $procReg0 -and [int]$procReg0.ProxyEnable -ne 0) {
            throw "Expected ProxyEnable=0 during 0-sub standby, got $($procReg0.ProxyEnable)"
        }
        $rt0 = Get-Content (Join-Path $TestData ".runtime-config.yaml") -Raw
        $tunBlock0 = ($rt0 -split "(?m)^(?=\S)" | Where-Object { $_ -match "^tun:" }) -join ""
        if ($tunBlock0 -notmatch "(?m)^\s*enable:\s*false\b") {
            throw "Expected TUN standby (tun.enable: false) before first subscription import, got:`n$tunBlock0"
        }
        Write-Host "  [OK] 0-subscription armed standby verified (defaults armed, ProxyEnable=0, TUN standby)"

        # Pick a free port, start local TCP HTTP responder for 1-step subscription import
        $probeLn = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
        $probeLn.Start()
        $subPort = ([System.Net.IPEndPoint]$probeLn.LocalEndpoint).Port
        $probeLn.Stop()

        $subJob = Start-Job -ScriptBlock {
            param([int]$Port)
            $yaml = @"
mixed-port: 17895
allow-lan: false
mode: rule
log-level: info
proxies:
  - name: "Verify-Node-01"
    type: socks5
    server: 198.51.100.25
    port: 1080
proxy-groups:
  - name: "Proxy"
    type: select
    proxies:
      - "Verify-Node-01"
      - "DIRECT"
rules:
  - MATCH,Proxy
"@
            $bodyBytes = [System.Text.Encoding]::UTF8.GetBytes($yaml)
            $ln = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, $Port)
            $ln.Start()
            try {
                for ($n = 0; $n -lt 6; $n++) {
                    $client = $ln.AcceptTcpClient()
                    try {
                        $stream = $client.GetStream()
                        $reader = New-Object System.IO.StreamReader($stream, [System.Text.Encoding]::ASCII, $false, 1024, $true)
                        while ($true) {
                            $line = $reader.ReadLine()
                            if ([string]::IsNullOrEmpty($line)) { break }
                        }
                        $hdr = "HTTP/1.1 200 OK`r`nContent-Type: text/yaml; charset=utf-8`r`nContent-Length: $($bodyBytes.Length)`r`nConnection: close`r`n`r`n"
                        $hdrBytes = [System.Text.Encoding]::ASCII.GetBytes($hdr)
                        $stream.Write($hdrBytes, 0, $hdrBytes.Length)
                        $stream.Write($bodyBytes, 0, $bodyBytes.Length)
                        $stream.Flush()
                    } catch {
                    } finally {
                        $client.Close()
                    }
                }
            } finally {
                $ln.Stop()
            }
        } -ArgumentList $subPort

        $subUrl = "http://127.0.0.1:$subPort/sub.yaml"
        $subUp = $false
        for ($i = 0; $i -lt 30; $i++) {
            Start-Sleep -Milliseconds 150
            try {
                $probeResp = Invoke-WebRequest -Uri $subUrl -UseBasicParsing -TimeoutSec 2
                if ($probeResp.StatusCode -eq 200) {
                    $subUp = $true
                    break
                }
            } catch {}
        }
        if (-not $subUp) {
            throw "Local subscription HTTP server did not start on port $subPort"
        }

        $impParams = @{ name = "OneClickSub"; url = $subUrl; select_after = $true } | ConvertTo-Json -Compress
        $impResp = Invoke-DaemonRpc -Method "upsert_subscription" -ParamsJson $impParams
        if (-not $impResp.ok) {
            throw "1-click upsert_subscription failed: $($impResp | ConvertTo-Json -Compress)"
        }

        $stAfter = Invoke-DaemonRpc -Method "get_state"
        if (-not $stAfter.ok -or -not $stAfter.data.core_running -or -not $stAfter.data.core_via_service) {
            throw "Expected core_via_service=true + core_running=true automatically on subscription import: $($stAfter | ConvertTo-Json -Compress)"
        }
        $procReg1 = Get-ItemProperty -Path $ProcInetSettings -ErrorAction Stop
        if ([int]$procReg1.ProxyEnable -ne 1 -or ($procReg1.ProxyServer -notlike "127.0.0.1:*")) {
            throw "Expected HKCU ProxyEnable=1 on 127.0.0.1:* automatically on import, got Enable=$($procReg1.ProxyEnable) Server=$($procReg1.ProxyServer)"
        }
        $rtAfter = Get-Content (Join-Path $TestData ".runtime-config.yaml") -Raw
        $tunBlock1 = ($rtAfter -split "(?m)^(?=\S)" | Where-Object { $_ -match "^tun:" }) -join ""
        if ($tunBlock1 -notmatch "(?m)^\s*enable:\s*true\b") {
            throw "Expected tun.enable: true in .runtime-config.yaml after 1st subscription import, got:`n$tunBlock1"
        }
        foreach ($want in @(
            "RULE-SET,clashfx-cn-apps-direct,DIRECT",
            "GEOSITE,cn,DIRECT",
            "GEOIP,CN,DIRECT,no-resolve",
            "GEOSITE,private,DIRECT",
            "GEOIP,LAN,DIRECT,no-resolve"
        )) {
            if ($rtAfter -notlike "*$want*") {
                throw "Expected '$want' in .runtime-config.yaml after 1st subscription import"
            }
        }
        Write-Host "  [OK] Zero-UAC 1-subscription auto-activation verified (TUN via ClashFXService + SysProxy + CN/LAN/App direct rules)!"
    } finally {
        if ($subJob) {
            Stop-Job $subJob -ErrorAction SilentlyContinue
            Remove-Job $subJob -Force -ErrorAction SilentlyContinue
        }
        if ($daemonProc -and -not $daemonProc.HasExited) {
            try { Invoke-DaemonRpc -Method "quit_daemon" | Out-Null } catch {}
            if (-not $daemonProc.WaitForExit(5000)) {
                try { $daemonProc.Kill() } catch {}
            }
        }
        Set-ItemProperty -Path $ProcInetSettings -Name ProxyEnable -Value $OrigProcProxyEnable -Type DWord -ErrorAction SilentlyContinue
        if ($null -ne $OrigProcProxyServer) {
            Set-ItemProperty -Path $ProcInetSettings -Name ProxyServer -Value $OrigProcProxyServer -Type String -ErrorAction SilentlyContinue
        } else {
            Remove-ItemProperty -Path $ProcInetSettings -Name ProxyServer -ErrorAction SilentlyContinue
        }
    }

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
    $leftFw = @(Get-NetFirewallRule -DisplayName "ClashFX - *" -ErrorAction SilentlyContinue)
    if ($leftFw.Count -gt 0) {
        throw "Leftover ClashFX Windows Firewall rules after uninstall: $($leftFw.DisplayName -join ', ')"
    }

    Write-Host "=== [ALL PASSED] Installer install + CRT/Firewall + zero-UAC 1-sub activation + in-place upgrade + P0-1 clean uninstall verified! ==="
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
