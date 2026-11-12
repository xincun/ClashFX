param(
    [string]$RepoRoot = "",
    [string]$AppVersion = "1.19.24"
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($RepoRoot)) {
    $RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
}

function Find-Iscc {
    $candidates = @(
        "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe",
        "${env:ProgramFiles}\Inno Setup 6\ISCC.exe",
        "${env:LOCALAPPDATA}\Programs\Inno Setup 6\ISCC.exe"
    )
    foreach ($c in $candidates) {
        if (Test-Path $c) { return $c }
    }
    $cmd = Get-Command iscc -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    return $null
}

$IsccExe = Find-Iscc
if (-not $IsccExe) {
    Write-Host "=== [Package] Installing Inno Setup 6.4.3 silently ==="
    $cachedInstaller = Join-Path $RepoRoot "windows\build\.cache\innosetup-6.4.3.exe"
    $innoSetupTmp = "C:\Temp\innosetup-6.4.3.exe"
    New-Item -ItemType Directory -Force -Path "C:\Temp" | Out-Null
    if (Test-Path $cachedInstaller) {
        Copy-Item -Force $cachedInstaller $innoSetupTmp
    } else {
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        Invoke-WebRequest -Uri "https://github.com/jrsoftware/issrc/releases/download/is-6_4_3/innosetup-6.4.3.exe" -OutFile $innoSetupTmp -UseBasicParsing
    }
    $p = Start-Process -FilePath $innoSetupTmp -ArgumentList @("/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/ALLUSERS") -Wait -PassThru
    Remove-Item -Force $innoSetupTmp -ErrorAction SilentlyContinue
    $IsccExe = Find-Iscc
    if (-not $IsccExe) {
        throw "ISCC.exe not found after Inno Setup 6 installation (exit code $($p.ExitCode))"
    }
}
Write-Host "=== [Package] Using Inno Setup Compiler: $IsccExe ==="

$DistDir = Join-Path $RepoRoot "windows\dist"
$IssFile = Join-Path $RepoRoot "windows\installer\clashfx.iss"

# Stage locally on C:\Temp so ISCC and Compress-Archive run at native SSD speed without UNC overhead
$PackWork = "C:\Temp\clashfx-package-work"
if (Test-Path $PackWork) { Remove-Item -Recurse -Force $PackWork }
New-Item -ItemType Directory -Force -Path "$PackWork\out" | Out-Null
Copy-Item -Force $IssFile "$PackWork\clashfx.iss"

foreach ($arch in @("arm64", "amd64")) {
    $srcDist = Join-Path $DistDir "windows-$arch"
    if (-not (Test-Path (Join-Path $srcDist "clashfx-ui.exe"))) {
        throw "Missing staged distribution at $srcDist. Run build-ui.ps1 first."
    }

    $localStage = Join-Path $PackWork "windows-$arch"
    New-Item -ItemType Directory -Force -Path $localStage | Out-Null
    Copy-Item -Recurse -Force (Join-Path $srcDist "*") $localStage

    Write-Host "=== [Package] Compiling ClashFX-Setup-$arch.exe ==="
    & $IsccExe "/Q" "/DMyArch=$arch" "/DAppVersion=$AppVersion" "/DSourceDir=$localStage" "/DOutputDir=$PackWork\out" "$PackWork\clashfx.iss"
    if ($LASTEXITCODE -ne 0) {
        throw "ISCC failed for $arch with exit code $LASTEXITCODE"
    }

    $setupOut = Join-Path $PackWork "out\ClashFX-Setup-$arch.exe"
    if (-not (Test-Path $setupOut)) {
        throw "Expected installer missing: $setupOut"
    }
    Copy-Item -Force $setupOut (Join-Path $DistDir "ClashFX-Setup-$arch.exe")

    $zipOut = Join-Path $PackWork "out\ClashFX-Portable-$arch.zip"
    if (Test-Path $zipOut) { Remove-Item -Force $zipOut }
    Compress-Archive -Path (Join-Path $localStage "*") -DestinationPath $zipOut -CompressionLevel Optimal
    Copy-Item -Force $zipOut (Join-Path $DistDir "ClashFX-Portable-$arch.zip")

    $setupMb = [Math]::Round((Get-Item $setupOut).Length / 1MB, 2)
    $zipMb = [Math]::Round((Get-Item $zipOut).Length / 1MB, 2)
    Write-Host "  [OK] ClashFX-Setup-$arch.exe ($setupMb MB) + ClashFX-Portable-$arch.zip ($zipMb MB)"
}

Remove-Item -Recurse -Force $PackWork -ErrorAction SilentlyContinue
Write-Host "=== [SUCCESS] Dual-arch installers & portable archives ready in $DistDir ==="
