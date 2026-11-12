param(
    [string]$BinRoot = "C:\Mac\Home\Desktop\workspace\github\ClashFX\windows\build\bin",
    [string]$TestBinRoot = "C:\Mac\Home\Desktop\workspace\github\ClashFX\windows\build\tests"
)

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"

$workRoot = "C:\Temp\clashfx-p1-2"
try {
    if (Test-Path $workRoot) {
        Remove-Item -Recurse -Force $workRoot
    }
    New-Item -ItemType Directory -Path "$workRoot\arm64", "$workRoot\amd64", "$workRoot\tmp" | Out-Null

    $env:GOTMPDIR = "$workRoot\tmp"
    $env:TEMP = "$workRoot\tmp"
    $env:TMP = "$workRoot\tmp"

    foreach ($arch in @("arm64", "amd64")) {
        Write-Host "=== Verifying P1-2 Multi-Node Ports, Watchdog & SpeedTest on windows/$arch ==="
        Copy-Item "$BinRoot\$arch\mihomo_core.exe" "$workRoot\$arch\mihomo_core.exe" -Force
        Copy-Item "$TestBinRoot\$arch\nodeservices.test.exe" "$workRoot\$arch\nodeservices.test.exe" -Force

        $env:CLASHFX_CORE_EXE = "$workRoot\$arch\mihomo_core.exe"
        & "$workRoot\$arch\nodeservices.test.exe" "-test.v"
        if ($LASTEXITCODE -ne 0) {
            throw "[$arch] nodeservices.test.exe failed with exit code $LASTEXITCODE"
        }
    }

    Write-Host "ALL P1-2 DUAL-ARCH VM CHECKS PASSED"
} finally {
    Remove-Item Env:\CLASHFX_CORE_EXE -ErrorAction SilentlyContinue
    if (Test-Path $workRoot) {
        Remove-Item -Recurse -Force $workRoot -ErrorAction SilentlyContinue
    }
}
