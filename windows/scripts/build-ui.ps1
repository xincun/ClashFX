param(
    [string]$RepoRoot = "",
    [string]$QtArm64Root = "C:\workspace\github\qt_out\qt6.8.3_arm64",
    [string]$QtX64Root = "C:\workspace\github\qt_out\qt6.8.3_x64",
    [ValidateSet("all", "arm64", "amd64")][string]$TargetArch = "all"
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($RepoRoot)) {
    $RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
}

$VsWhere = "${env:ProgramFiles(x86)}\Microsoft Visual Studio\Installer\vswhere.exe"
if (-not (Test-Path $VsWhere)) {
    throw "vswhere.exe not found at $VsWhere"
}
$VsPath = (& $VsWhere -latest -products * -property installationPath).Trim()
$VcVarsAll = Join-Path $VsPath "VC\Auxiliary\Build\vcvarsall.bat"
$NinjaExe = Join-Path $VsPath "Common7\IDE\CommonExtensions\Microsoft\CMake\Ninja\ninja.exe"
if (-not (Test-Path $NinjaExe)) {
    $NinjaExe = (Get-Command ninja -ErrorAction Stop).Source
}
$CMakeExe = Join-Path $VsPath "Common7\IDE\CommonExtensions\Microsoft\CMake\CMake\bin\cmake.exe"
if (-not (Test-Path $CMakeExe)) {
    $CMakeExe = (Get-Command cmake -ErrorAction Stop).Source
}

$WebView2SdkDir = Join-Path $RepoRoot "windows\build\.cache\webview2"
if (-not (Test-Path (Join-Path $WebView2SdkDir "build\native\include\WebView2.h"))) {
    throw "WebView2 SDK not extracted at $WebView2SdkDir. Run python3 windows/scripts/fetch-webview2-sdk.py first."
}

function Assert-PeArch {
    param(
        [string]$FilePath,
        [uint16]$ExpectedMachine,
        [string]$Label
    )
    if (-not (Test-Path $FilePath)) {
        throw "Missing expected binary: $FilePath"
    }
    $bytes = [System.IO.File]::ReadAllBytes($FilePath)
    if ($bytes.Length -lt 0x40 -or $bytes[0] -ne 0x4D -or $bytes[1] -ne 0x5A) {
        throw "$FilePath is not a valid PE file"
    }
    $peOffset = [System.BitConverter]::ToInt32($bytes, 0x3C)
    $machine = [System.BitConverter]::ToUInt16($bytes, $peOffset + 4)
    if ($machine -ne $ExpectedMachine) {
        throw ("PE machine mismatch for {0}: got 0x{1:X4}, expected 0x{2:X4} ({3})" -f $FilePath, $machine, $ExpectedMachine, $Label)
    }
}

$WorkRoot = "C:\Temp\clashfx-ui-build"
New-Item -ItemType Directory -Force -Path $WorkRoot | Out-Null

# Copy UI source + WebView2 SDK to local SSD so MSVC/MOC/RC/windeployqt avoid UNC path quirks and build fast
$LocalSrc = Join-Path $WorkRoot "ui-qt-src"
$LocalWv2 = Join-Path $WorkRoot "webview2"
if (Test-Path $LocalSrc) { Remove-Item -Recurse -Force $LocalSrc }
if (Test-Path $LocalWv2) { Remove-Item -Recurse -Force $LocalWv2 }
Copy-Item -Recurse -Force (Join-Path $RepoRoot "windows\ui-qt") $LocalSrc
Copy-Item -Recurse -Force $WebView2SdkDir $LocalWv2

$isArmHost = ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") -or ($env:PROCESSOR_ARCHITEW6432 -eq "ARM64")
$Targets = @(
    @{
        Arch          = "arm64"
        DistName      = "windows-arm64"
        VcVarsArg     = if ($isArmHost) { "arm64" } else { "amd64_arm64" }
        WebView2Arch  = "arm64"
        QtRoot        = $QtArm64Root
        PeMachine     = [uint16]0xAA64
    },
    @{
        Arch          = "amd64"
        DistName      = "windows-amd64"
        VcVarsArg     = if ($isArmHost) { "arm64_x64" } else { "x64" }
        WebView2Arch  = "x64"
        QtRoot        = $QtX64Root
        PeMachine     = [uint16]0x8664
    }
)
if ($TargetArch -ne "all") {
    $Targets = @($Targets | Where-Object { $_.Arch -eq $TargetArch })
}

foreach ($t in $Targets) {
    $arch = $t.Arch
    $qtRoot = $t.QtRoot
    if (-not (Test-Path $qtRoot) -and -not [string]::IsNullOrWhiteSpace($env:QT_ROOT_DIR) -and (Test-Path $env:QT_ROOT_DIR)) {
        $qtRoot = $env:QT_ROOT_DIR
    }
    if (-not (Test-Path $qtRoot)) {
        throw "Qt root missing for ${arch}: $qtRoot"
    }
    Write-Host "=== [ClashFX UI] Building $arch ($($t.VcVarsArg)) against $qtRoot ==="

    $buildDir = Join-Path $WorkRoot "build-$arch"
    $stageDir = Join-Path $WorkRoot "stage-$arch"
    if (Test-Path $buildDir) { Remove-Item -Recurse -Force $buildDir }
    if (Test-Path $stageDir) { Remove-Item -Recurse -Force $stageDir }
    New-Item -ItemType Directory -Force -Path $buildDir | Out-Null
    New-Item -ItemType Directory -Force -Path $stageDir | Out-Null

    $cmdFile = Join-Path $WorkRoot "run-cmake-$arch.cmd"
    $cmdBody = @"
@echo off
call "$VcVarsAll" $($t.VcVarsArg)
if errorlevel 1 exit /b 1
"$CMakeExe" -G Ninja -DCMAKE_MAKE_PROGRAM="$NinjaExe" -DCMAKE_BUILD_TYPE=Release -DCMAKE_PREFIX_PATH="$qtRoot" -DWEBVIEW2_SDK_DIR="$LocalWv2" -DWEBVIEW2_ARCH="$($t.WebView2Arch)" -S "$LocalSrc" -B "$buildDir"
if errorlevel 1 exit /b 1
"$CMakeExe" --build "$buildDir" --parallel
if errorlevel 1 exit /b 1
"@
    [System.IO.File]::WriteAllText($cmdFile, $cmdBody, [System.Text.Encoding]::ASCII)

    & cmd.exe /c "`"$cmdFile`""
    if ($LASTEXITCODE -ne 0) {
        throw "CMake/Ninja build failed for $arch (exit code $LASTEXITCODE)"
    }

    $builtUiExe = Join-Path $buildDir "clashfx-ui.exe"
    Copy-Item -Force $builtUiExe (Join-Path $stageDir "clashfx-ui.exe")

    $windeploy = Join-Path $qtRoot "bin\windeployqt.exe"
    $deployCmdFile = Join-Path $WorkRoot "run-deploy-$arch.cmd"
    $deployBody = @"
@echo off
call "$VcVarsAll" $($t.VcVarsArg)
if errorlevel 1 exit /b 1
set PATH=$qtRoot\bin;%PATH%
"$windeploy" --release --no-translations --no-opengl-sw --no-system-d3d-compiler --no-compiler-runtime "$stageDir\clashfx-ui.exe"
if errorlevel 1 exit /b 1
"@
    [System.IO.File]::WriteAllText($deployCmdFile, $deployBody, [System.Text.Encoding]::ASCII)
    & cmd.exe /c "`"$deployCmdFile`""
    if ($LASTEXITCODE -ne 0) {
        throw "windeployqt failed for $arch (exit code $LASTEXITCODE)"
    }

    # Bundle app-local MSVC C++ CRT DLLs matching target architecture (without vc_redist.exe bloat)
    $msvcRedistBase = Join-Path $VsPath "VC\Redist\MSVC"
    $crtDir = $null
    if (Test-Path $msvcRedistBase) {
        $verDirs = Get-ChildItem -Path $msvcRedistBase -Directory |
            Where-Object { $_.Name -match '^\d+\.\d+' } |
            Sort-Object { [version]$_.Name } -Descending
        foreach ($vd in $verDirs) {
            foreach ($archDir in @(
                (Join-Path $vd.FullName $t.WebView2Arch),
                (Join-Path $vd.FullName ("onecore\" + $t.WebView2Arch))
            )) {
                if (Test-Path $archDir) {
                    $candidateCrt = Get-ChildItem -Path $archDir -Directory -Filter "Microsoft.VC*.CRT" | Select-Object -First 1
                    if ($candidateCrt) {
                        $crtDir = $candidateCrt.FullName
                        break
                    }
                }
            }
            if ($crtDir) { break }
        }
    }
    if (-not $crtDir) {
        throw "Could not locate Microsoft.VC*.CRT redist DLLs for $($t.WebView2Arch) under $msvcRedistBase"
    }
    Get-ChildItem -Path $crtDir -Filter "*.dll" -File | ForEach-Object {
        Copy-Item -Force $_.FullName (Join-Path $stageDir $_.Name)
    }

    # Copy Go binaries + Wintun for this architecture
    $binDir = Join-Path $RepoRoot "windows\build\bin\$arch"
    foreach ($f in @("mihomo_core.exe", "wintun.dll", "clashfx-service.exe", "clashfx-daemon.exe")) {
        $srcBin = Join-Path $binDir $f
        if (-not (Test-Path $srcBin)) {
            throw "Required backend binary missing for ${arch}: $srcBin"
        }
        Copy-Item -Force $srcBin (Join-Path $stageDir $f)
    }

    # Copy bundled resources (MetaCubeXD dashboard, clashfx-compat.js, GEO archives, app icon)
    $stageRes = Join-Path $stageDir "resources"
    New-Item -ItemType Directory -Force -Path $stageRes | Out-Null
    Copy-Item -Recurse -Force (Join-Path $RepoRoot "macos\resources\dashboard") (Join-Path $stageRes "dashboard")
    Copy-Item -Force (Join-Path $RepoRoot "macos\resources\DashboardCompatibility\clashfx-compat.js") (Join-Path $stageRes "clashfx-compat.js")
    Copy-Item -Force (Join-Path $RepoRoot "macos\resources\Country.mmdb.gz") (Join-Path $stageRes "Country.mmdb.gz")
    Copy-Item -Force (Join-Path $RepoRoot "macos\resources\geosite.dat.gz") (Join-Path $stageRes "geosite.dat.gz")
    Copy-Item -Force (Join-Path $RepoRoot "windows\ui-qt\resources\clashfx.ico") (Join-Path $stageRes "clashfx.ico")
    $stageUi = Join-Path $stageRes "ui"
    if (Test-Path $stageUi) { Remove-Item -Recurse -Force $stageUi }
    Copy-Item -Recurse -Force (Join-Path $RepoRoot "windows\ui-qt\web") $stageUi

    # Verify PE architecture headers strictly on all core executables & DLLs
    foreach ($peName in @("clashfx-ui.exe", "clashfx-daemon.exe", "clashfx-service.exe", "mihomo_core.exe", "wintun.dll", "Qt6Widgets.dll", "vcruntime140.dll", "msvcp140.dll")) {
        Assert-PeArch -FilePath (Join-Path $stageDir $peName) -ExpectedMachine $t.PeMachine -Label $arch
    }

    # Sync staged folder back to repo windows\dist\windows-<arch>
    $distDir = Join-Path $RepoRoot ("windows\dist\" + $t.DistName)
    if (Test-Path $distDir) { Remove-Item -Recurse -Force $distDir }
    New-Item -ItemType Directory -Force -Path $distDir | Out-Null
    Copy-Item -Recurse -Force (Join-Path $stageDir "*") $distDir

    Write-Host "[OK] Staged and PE-verified $($t.DistName) -> $distDir"
}

Remove-Item -Recurse -Force $WorkRoot -ErrorAction SilentlyContinue
Write-Host "=== [SUCCESS] Dual-architecture ClashFX Qt6 + WebView2 UI builds complete! ==="
