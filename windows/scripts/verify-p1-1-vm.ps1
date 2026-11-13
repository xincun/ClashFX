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

$workRoot = "C:\Temp\clashfx-p1-1"
try {
    if (Test-Path $workRoot) {
        Remove-Item -Recurse -Force $workRoot
    }
    New-Item -ItemType Directory -Path "$workRoot\arm64", "$workRoot\amd64", "$workRoot\tmp" | Out-Null

    $env:GOTMPDIR = "$workRoot\tmp"
    $env:TEMP = "$workRoot\tmp"
    $env:TMP = "$workRoot\tmp"

    foreach ($arch in @("arm64", "amd64")) {
        Write-Host "=== Verifying P1-1 AI Proxy Lock on windows/$arch ==="
        Copy-Item "$BinRoot\$arch\mihomo_core.exe" "$workRoot\$arch\mihomo_core.exe" -Force
        Copy-Item "$TestBinRoot\$arch\ailock.test.exe" "$workRoot\$arch\ailock.test.exe" -Force

        $env:CLASHFX_CORE_EXE = "$workRoot\$arch\mihomo_core.exe"
        & "$workRoot\$arch\ailock.test.exe" "-test.v"
        if ($LASTEXITCODE -ne 0) {
            throw "[$arch] ailock.test.exe failed with exit code $LASTEXITCODE"
        }
    }

    Write-Host "ALL P1-1 DUAL-ARCH VM CHECKS PASSED"
} finally {
    Remove-Item Env:\CLASHFX_CORE_EXE -ErrorAction SilentlyContinue
    if (Test-Path $workRoot) {
        Remove-Item -Recurse -Force $workRoot -ErrorAction SilentlyContinue
    }
}
