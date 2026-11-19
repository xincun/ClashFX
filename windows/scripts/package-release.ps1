param(
    [string]$RepoRoot = "",
    [string]$AppVersion = "1.19.24",
    [ValidateSet("all", "arm64", "amd64")][string]$TargetArch = "all"
)

$ErrorActionPreference = "Stop"
$AppVersion = $AppVersion.Trim().TrimStart('v', 'V')
if ([string]::IsNullOrWhiteSpace($AppVersion)) {
    $AppVersion = "1.19.24"
}

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

$ExpectedInnoSha256 = "F3C42116542C4CC57263C5BA6C4FEABFC49FE771F2F98A79D2F7628B8762723B"

function Test-Sha256 {
    param([string]$FilePath, [string]$ExpectedHash)
    if (-not (Test-Path $FilePath)) { return $false }
    $actual = (Get-FileHash -Path $FilePath -Algorithm SHA256).Hash.ToUpperInvariant()
    return ($actual -eq $ExpectedHash.ToUpperInvariant())
}

function Test-MicrosoftSignedBinary {
    param([string]$FilePath)
    if (-not (Test-Path $FilePath)) { return $false }
    $sig = Get-AuthenticodeSignature -FilePath $FilePath
    return ($sig.Status -eq 'Valid' -and $sig.SignerCertificate -and ($sig.SignerCertificate.Subject -match 'CN=Microsoft Corporation'))
}

$IsccExe = Find-Iscc
if (-not $IsccExe) {
    Write-Host "=== [Package] Installing Inno Setup 6.4.3 silently ==="
    $cachedInstaller = Join-Path $RepoRoot "windows\build\.cache\innosetup-6.4.3.exe"
    $innoSetupTmp = "C:\Temp\innosetup-6.4.3.exe"
    New-Item -ItemType Directory -Force -Path "C:\Temp" | Out-Null
    if ((Test-Path $cachedInstaller) -and (Test-Sha256 -FilePath $cachedInstaller -ExpectedHash $ExpectedInnoSha256)) {
        Copy-Item -Force $cachedInstaller $innoSetupTmp
    } else {
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        Invoke-WebRequest -Uri "https://github.com/jrsoftware/issrc/releases/download/is-6_4_3/innosetup-6.4.3.exe" -OutFile $innoSetupTmp -UseBasicParsing
    }
    if (-not (Test-Sha256 -FilePath $innoSetupTmp -ExpectedHash $ExpectedInnoSha256)) {
        $actualHash = (Get-FileHash -Path $innoSetupTmp -Algorithm SHA256).Hash
        Remove-Item -Force $innoSetupTmp -ErrorAction SilentlyContinue
        throw "SHA256 mismatch for innosetup-6.4.3.exe: expected $ExpectedInnoSha256, got $actualHash"
    }
    $innoLog = "C:\Temp\inno-install.log"
    Remove-Item -Force $innoLog -ErrorAction SilentlyContinue
    $p = Start-Process -FilePath $innoSetupTmp -ArgumentList @("/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/ALLUSERS", "/LOG=`"$innoLog`"") -PassThru
    if (-not $p.WaitForExit(120000)) {
        try { $p.Kill() } catch {}
        $tail = if (Test-Path $innoLog) { (Get-Content $innoLog -Tail 30 | Out-String) } else { "(no log)" }
        throw "Inno Setup installer timed out after 120s. Log tail:`n$tail"
    }
    Remove-Item -Force $innoSetupTmp -ErrorAction SilentlyContinue
    $IsccExe = Find-Iscc
    if (-not $IsccExe) {
        throw "ISCC.exe not found after Inno Setup 6 installation (exit code $($p.ExitCode))"
    }
}
Write-Host "=== [Package] Using Inno Setup Compiler: $IsccExe ==="

$DistDir = Join-Path $RepoRoot "windows\dist"
$InstallerDir = Join-Path $RepoRoot "windows\installer"
$CacheDir = Join-Path $RepoRoot "windows\build\.cache"
New-Item -ItemType Directory -Force -Path $CacheDir | Out-Null

$Wv2Bootstrapper = Join-Path $CacheDir "MicrosoftEdgeWebView2Setup.exe"
$needDownloadWv2 = $true
if ((Test-Path $Wv2Bootstrapper) -and (Get-Item $Wv2Bootstrapper).Length -ge 100KB) {
    if (Test-MicrosoftSignedBinary -FilePath $Wv2Bootstrapper) {
        $needDownloadWv2 = $false
    } else {
        Write-Warning "Cached MicrosoftEdgeWebView2Setup.exe failed Authenticode verification; re-downloading..."
        Remove-Item -Force $Wv2Bootstrapper -ErrorAction SilentlyContinue
    }
}
if ($needDownloadWv2) {
    Write-Host "=== [Package] Downloading MicrosoftEdgeWebView2Setup.exe bootstrapper ==="
    $wv2Tmp = "C:\Temp\MicrosoftEdgeWebView2Setup.exe.tmp"
    New-Item -ItemType Directory -Force -Path "C:\Temp" | Out-Null
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Invoke-WebRequest -Uri "https://go.microsoft.com/fwlink/p/?LinkId=2124703" -OutFile $wv2Tmp -UseBasicParsing
    if ((Get-Item $wv2Tmp).Length -lt 100KB) {
        Remove-Item -Force $wv2Tmp -ErrorAction SilentlyContinue
        throw "Downloaded MicrosoftEdgeWebView2Setup.exe looks invalid/too small"
    }
    if (-not (Test-MicrosoftSignedBinary -FilePath $wv2Tmp)) {
        Remove-Item -Force $wv2Tmp -ErrorAction SilentlyContinue
        throw "Downloaded MicrosoftEdgeWebView2Setup.exe failed Microsoft Authenticode signature verification"
    }
    Move-Item -Force $wv2Tmp $Wv2Bootstrapper
}

# Stage locally on C:\Temp so ISCC and Compress-Archive run at native SSD speed without UNC overhead
$PackWork = "C:\Temp\clashfx-package-work"
if (Test-Path $PackWork) { Remove-Item -Recurse -Force $PackWork }
New-Item -ItemType Directory -Force -Path "$PackWork\out" | Out-Null
Copy-Item -Force (Join-Path $InstallerDir "*") $PackWork
Copy-Item -Force $Wv2Bootstrapper (Join-Path $PackWork "MicrosoftEdgeWebView2Setup.exe")

$arches = if ($TargetArch -eq "all") { @("arm64", "amd64") } else { @($TargetArch) }
foreach ($arch in $arches) {
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
