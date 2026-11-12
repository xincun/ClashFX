#!/usr/bin/env python3
"""Build Windows arm64 & amd64 mihomo_core.exe + verified official wintun.dll.

Outputs:
  windows/build/bin/arm64/{mihomo_core.exe, wintun.dll}
  windows/build/bin/amd64/{mihomo_core.exe, wintun.dll}

Guarantees:
  - Uses ClashFX's fail-closed core_overlay (pinned Mihomo v1.19.24 + sing v0.5.7)
  - Compiles with `-tags with_gvisor` and `-trimpath`
  - Verifies official WireGuard Wintun 0.14.1 archive SHA256
  - Strictly validates PE/COFF Machine headers (`0xAA64` for arm64, `0x8664` for amd64)
"""

from __future__ import annotations

import argparse
import datetime
import hashlib
import os
import pathlib
import shutil
import struct
import subprocess
import sys
import urllib.request
import zipfile

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
GOCLASH_ROOT = REPO_ROOT / "ClashFX" / "goClash"
WINDOWS_BUILD_ROOT = REPO_ROOT / "windows" / "build"
CACHE_DIR = WINDOWS_BUILD_ROOT / ".cache"

WINTUN_URL = "https://www.wintun.net/builds/wintun-0.14.1.zip"
WINTUN_ZIP_SHA256 = "07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51"

PE_MACHINE_BY_ARCH = {
    "arm64": 0xAA64,  # IMAGE_FILE_MACHINE_ARM64
    "amd64": 0x8664,  # IMAGE_FILE_MACHINE_AMD64
}

WINTUN_ZIP_MEMBER_BY_ARCH = {
    "arm64": "wintun/bin/arm64/wintun.dll",
    "amd64": "wintun/bin/amd64/wintun.dll",
}

sys.path.insert(0, str(GOCLASH_ROOT))
from core_overlay import core_modfile  # noqa: E402


def detect_mihomo_version() -> str:
    for line in (GOCLASH_ROOT / "go.mod").read_text(encoding="utf-8").splitlines():
        if "github.com/metacubex/mihomo" in line:
            parts = line.split()
            if len(parts) >= 2:
                return parts[-1].lstrip("v")
    raise RuntimeError("Unable to detect Mihomo version from ClashFX/goClash/go.mod")


def sha256_file(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(65536), b""):
            digest.update(chunk)
    return digest.hexdigest()


def verify_pe_machine(path: pathlib.Path, expected_arch: str) -> None:
    expected_machine = PE_MACHINE_BY_ARCH[expected_arch]
    data = path.read_bytes()
    if len(data) < 0x40 or data[:2] != b"MZ":
        raise RuntimeError(f"{path} is not a valid DOS/PE binary (missing MZ header)")
    (pe_offset,) = struct.unpack_from("<I", data, 0x3C)
    if pe_offset + 6 > len(data) or data[pe_offset : pe_offset + 4] != b"PE\x00\x00":
        raise RuntimeError(f"{path} has an invalid PE signature at offset {pe_offset:#x}")
    (machine,) = struct.unpack_from("<H", data, pe_offset + 4)
    if machine != expected_machine:
        raise RuntimeError(
            f"PE architecture mismatch in {path}: got {machine:#06x}, "
            f"expected {expected_machine:#06x} ({expected_arch})"
        )


def ensure_wintun_zip() -> pathlib.Path:
    CACHE_DIR.mkdir(parents=True, exist_ok=True)
    archive_path = CACHE_DIR / "wintun-0.14.1.zip"
    if archive_path.is_file():
        actual = sha256_file(archive_path)
        if actual == WINTUN_ZIP_SHA256:
            return archive_path
        archive_path.unlink()

    print(f"Downloading official Wintun 0.14.1 from {WINTUN_URL} ...")
    tmp_path = archive_path.with_suffix(".zip.part")
    req = urllib.request.Request(WINTUN_URL, headers={"User-Agent": "ClashFX-Build/1.0"})
    with urllib.request.urlopen(req, timeout=60) as resp, tmp_path.open("wb") as out:
        shutil.copyfileobj(resp, out)
    actual = sha256_file(tmp_path)
    if actual != WINTUN_ZIP_SHA256:
        tmp_path.unlink(missing_ok=True)
        raise RuntimeError(
            f"Wintun SHA256 verification failed: expected {WINTUN_ZIP_SHA256}, got {actual}"
        )
    tmp_path.replace(archive_path)
    return archive_path


def extract_wintun_dll(archive_path: pathlib.Path, arch: str, dest_dir: pathlib.Path) -> pathlib.Path:
    member = WINTUN_ZIP_MEMBER_BY_ARCH[arch]
    target = dest_dir / "wintun.dll"
    with zipfile.ZipFile(archive_path, "r") as zf:
        with zf.open(member) as src, target.open("wb") as dst:
            shutil.copyfileobj(src, dst)
    verify_pe_machine(target, arch)
    return target


def build_core_for_arch(modfile: str, version: str, build_time: str, arch: str, out_dir: pathlib.Path) -> pathlib.Path:
    out_dir.mkdir(parents=True, exist_ok=True)
    output_exe = out_dir / "mihomo_core.exe"
    ldflags = (
        f'-s -w '
        f'-X "github.com/metacubex/mihomo/constant.Version={version}" '
        f'-X "github.com/metacubex/mihomo/constant.BuildTime={build_time}"'
    )
    env = dict(
        os.environ,
        GOOS="windows",
        GOARCH=arch,
        CGO_ENABLED="0",
    )
    cmd = [
        "go",
        "build",
        f"-modfile={modfile}",
        "-trimpath",
        "-tags",
        "with_gvisor",
        "-ldflags",
        ldflags,
        "-o",
        str(output_exe),
        "./mihomo-bin/",
    ]
    print(f"Building mihomo_core.exe for windows/{arch} ...")
    subprocess.check_call(cmd, cwd=GOCLASH_ROOT, env=env)
    verify_pe_machine(output_exe, arch)
    return output_exe


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--arch",
        choices=["all", "arm64", "amd64"],
        default="all",
        help="target Windows architecture(s) to build",
    )
    parser.add_argument(
        "--output-root",
        type=pathlib.Path,
        default=WINDOWS_BUILD_ROOT / "bin",
        help="output root directory containing <arch>/ subdirectories",
    )
    parser.add_argument(
        "--build-windows-test",
        action="store_true",
        help="also cross-compile mihomo_core_test.exe per arch for VM execution",
    )
    args = parser.parse_args()

    arches = ["arm64", "amd64"] if args.arch == "all" else [args.arch]
    version = detect_mihomo_version()
    build_time = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d-%H%M")

    wintun_zip = ensure_wintun_zip()

    with core_modfile() as modfile:
        for arch in arches:
            arch_dir = args.output_root.resolve() / arch
            core_exe = build_core_for_arch(modfile, version, build_time, arch, arch_dir)
            wintun_dll = extract_wintun_dll(wintun_zip, arch, arch_dir)
            print(
                f"  [OK] windows/{arch}: {core_exe.name} ({core_exe.stat().st_size // 1024} KiB, sha256={sha256_file(core_exe)[:12]}), "
                f"{wintun_dll.name} ({wintun_dll.stat().st_size // 1024} KiB, sha256={sha256_file(wintun_dll)[:12]})"
            )
            if args.build_windows_test:
                test_exe = arch_dir / "mihomo_bin_test.exe"
                env = dict(os.environ, GOOS="windows", GOARCH=arch, CGO_ENABLED="0")
                subprocess.check_call(
                    [
                        "go",
                        "test",
                        f"-modfile={modfile}",
                        "-tags",
                        "with_gvisor",
                        "-c",
                        "-o",
                        str(test_exe),
                        "./mihomo-bin/",
                    ],
                    cwd=GOCLASH_ROOT,
                    env=env,
                )
                verify_pe_machine(test_exe, arch)
                print(f"  [OK] windows/{arch} test binary: {test_exe.name}")

    return 0


if __name__ == "__main__":
    sys.exit(main())
