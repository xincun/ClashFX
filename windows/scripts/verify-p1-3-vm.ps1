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

$workRoot = "C:\Temp\clashfx-p1-3"
try {
    if (Test-Path $workRoot) {
        Remove-Item -Recurse -Force $workRoot
    }
    New-Item -ItemType Directory -Path "$workRoot\arm64", "$workRoot\amd64", "$workRoot\tmp" | Out-Null

    $env:GOTMPDIR = "$workRoot\tmp"
    $env:TEMP = "$workRoot\tmp"
    $env:TMP = "$workRoot\tmp"

    foreach ($arch in @("arm64", "amd64")) {
        Write-Host "=== Verifying P1-3 clashfx-daemon & features on windows/$arch ==="
        Copy-Item "$BinRoot\$arch\mihomo_core.exe" "$workRoot\$arch\mihomo_core.exe" -Force
        Copy-Item "$BinRoot\$arch\clashfx-daemon.exe" "$workRoot\$arch\clashfx-daemon.exe" -Force
        Copy-Item "$TestBinRoot\$arch\features.test.exe" "$workRoot\$arch\features.test.exe" -Force
        Copy-Item "$TestBinRoot\$arch\daemon.test.exe" "$workRoot\$arch\daemon.test.exe" -Force

        $ver = (& "$workRoot\$arch\clashfx-daemon.exe" version | Out-String).Trim()
        if ($LASTEXITCODE -ne 0 -or $ver -notmatch "clashfx-daemon") {
            throw "[$arch] clashfx-daemon.exe version failed: $ver"
        }
        Write-Host "  [$arch] $ver"

        & "$workRoot\$arch\features.test.exe" "-test.v"
        if ($LASTEXITCODE -ne 0) {
            throw "[$arch] features.test.exe failed"
        }

        $env:CLASHFX_CORE_EXE = "$workRoot\$arch\mihomo_core.exe"
        & "$workRoot\$arch\daemon.test.exe" "-test.v"
        if ($LASTEXITCODE -ne 0) {
            throw "[$arch] daemon.test.exe failed"
        }
    }

    Write-Host "ALL P1-3 DUAL-ARCH VM CHECKS PASSED"
} finally {
    Remove-Item Env:\CLASHFX_CORE_EXE -ErrorAction SilentlyContinue
    if (Test-Path $workRoot) {
        Remove-Item -Recurse -Force $workRoot -ErrorAction SilentlyContinue
    }
}
