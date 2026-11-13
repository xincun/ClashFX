param(
    [string]$BinRoot = "",
    [string]$TestBinRoot = ""
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

if ([string]::IsNullOrWhiteSpace($BinRoot)) {
    $BinRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\build\bin")).Path
}
if ([string]::IsNullOrWhiteSpace($TestBinRoot)) {
    $TestBinRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\build\tests")).Path
}

$workRoot = "C:\Temp\clashfx-p0-3"
$reservedPort = 19190
$exclusiveSock = $null

try {
    if (Test-Path $workRoot) {
        Remove-Item -Recurse -Force $workRoot
    }
    New-Item -ItemType Directory -Path "$workRoot\arm64", "$workRoot\amd64", "$workRoot\tmp" | Out-Null

    # Hold a real Windows SO_EXCLUSIVEADDRUSE wildcard bind on 0.0.0.0:19190 so
    # 127.0.0.1:19190 fails in Winsock with genuine WSAEACCES (10013), identical
    # to Windows WinNAT / HNS / Hyper-V administered port exclusions.
    $exclusiveSock = New-Object System.Net.Sockets.Socket(
        [System.Net.Sockets.AddressFamily]::InterNetwork,
        [System.Net.Sockets.SocketType]::Stream,
        [System.Net.Sockets.ProtocolType]::Tcp
    )
    $exclusiveSock.ExclusiveAddressUse = $true
    $exclusiveSock.Bind([System.Net.IPEndPoint]::new([System.Net.IPAddress]::Any, $reservedPort))
    Write-Host "[winsock] Bound SO_EXCLUSIVEADDRUSE on 0.0.0.0:$reservedPort (Loopback bind -> WSAEACCES 10013)"

    $env:CLASHFX_TEST_EXCLUDED_PORT = "$reservedPort"
    $env:GOTMPDIR = "$workRoot\tmp"
    $env:TEMP = "$workRoot\tmp"
    $env:TMP = "$workRoot\tmp"

    foreach ($arch in @("arm64", "amd64")) {
        Write-Host "=== Verifying P0-3 on windows/$arch ==="
        Copy-Item "$BinRoot\$arch\mihomo_core.exe" "$workRoot\$arch\mihomo_core.exe" -Force
        Copy-Item "$TestBinRoot\$arch\*.test.exe" "$workRoot\$arch\" -Force

        $env:CLASHFX_CORE_EXE = "$workRoot\$arch\mihomo_core.exe"
        foreach ($suite in @("portprobe.test.exe", "profile.test.exe", "subscription.test.exe", "transaction.test.exe")) {
            Write-Host "  [$arch] Running $suite ..."
            & "$workRoot\$arch\$suite" "-test.v"
            if ($LASTEXITCODE -ne 0) {
                throw "[$arch] $suite failed with exit code $LASTEXITCODE"
            }
        }
    }

    Write-Host "ALL P0-3 DUAL-ARCH VM CHECKS PASSED"
} finally {
    if ($null -ne $exclusiveSock) {
        $exclusiveSock.Dispose()
    }
    Remove-Item Env:\CLASHFX_CORE_EXE -ErrorAction SilentlyContinue
    Remove-Item Env:\CLASHFX_TEST_EXCLUDED_PORT -ErrorAction SilentlyContinue
    if (Test-Path $workRoot) {
        Remove-Item -Recurse -Force $workRoot -ErrorAction SilentlyContinue
    }
}
