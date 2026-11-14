#!/usr/bin/env python3
"""Build Windows arm64 & amd64 stock upstream mihomo_core.exe + verified official wintun.dll.

Outputs:
  windows/build/bin/arm64/{mihomo_core.exe, wintun.dll}
  windows/build/bin/amd64/{mihomo_core.exe, wintun.dll}

Guarantees:
  - Compiles 100% unmodified upstream github.com/metacubex/mihomo (no custom CLI flags or source overlays)
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
CORE_ENGINE_ROOT = REPO_ROOT / "core-engine"
WINDOWS_BUILD_ROOT = REPO_ROOT / "windows" / "build"
DIST_ROOT = REPO_ROOT / "windows" / "dist"
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


def resolve_upstream_mihomo() -> tuple[pathlib.Path, str]:
    out = subprocess.check_output(
        ["go", "list", "-m", "-f", "{{.Dir}}\t{{.Version}}", "github.com/metacubex/mihomo"],
        cwd=CORE_ENGINE_ROOT,
        text=True,
    ).strip()
    parts = out.split("\t")
    if len(parts) != 2 or not parts[0]:
        raise RuntimeError(f"Failed to resolve upstream github.com/metacubex/mihomo module: {out!r}")
    mod_dir = pathlib.Path(parts[0])
    if not (mod_dir / "main.go").is_file():
        raise RuntimeError(f"Upstream Mihomo module directory missing main.go: {mod_dir}")
    return mod_dir, parts[1]


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
    if path.suffix.lower() == ".exe":
        (opt_magic,) = struct.unpack_from("<H", data, pe_offset + 24)
        if opt_magic != 0x020B:
            raise RuntimeError(f"PE OptionalHeader magic mismatch in {path}: got {opt_magic:#06x}, expected 0x020b")
        (subsystem,) = struct.unpack_from("<H", data, pe_offset + 92)
        if subsystem != 2:
            raise RuntimeError(f"PE Subsystem mismatch in {path}: got {subsystem}, expected 2 (WINDOWS_GUI)")


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


def build_core_for_arch(
    mihomo_dir: pathlib.Path, version: str, build_time: str, arch: str, out_dir: pathlib.Path
) -> pathlib.Path:
    out_dir.mkdir(parents=True, exist_ok=True)
    output_exe = out_dir / "mihomo_core.exe"
    ldflags = (
        f"-H=windowsgui -s -w "
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
        "-trimpath",
        "-tags",
        "with_gvisor",
        "-ldflags",
        ldflags,
        "-o",
        str(output_exe),
        ".",
    ]
    print(f"Building stock upstream mihomo_core.exe ({version}) for windows/{arch} ...")
    subprocess.check_call(cmd, cwd=mihomo_dir, env=env)
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
    args = parser.parse_args()

    arches = ["arm64", "amd64"] if args.arch == "all" else [args.arch]
    mihomo_dir, version = resolve_upstream_mihomo()
    build_time = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%d-%H%M")

    wintun_zip = ensure_wintun_zip()

    for arch in arches:
        arch_dir = args.output_root.resolve() / arch
        core_exe = build_core_for_arch(mihomo_dir, version, build_time, arch, arch_dir)
        wintun_dll = extract_wintun_dll(wintun_zip, arch, arch_dir)
        dist_stage = DIST_ROOT / f"windows-{arch}"
        if dist_stage.is_dir():
            shutil.copy2(core_exe, dist_stage / core_exe.name)
        print(
            f"  [OK] windows/{arch}: {core_exe.name} ({core_exe.stat().st_size // 1024} KiB, sha256={sha256_file(core_exe)[:12]}), "
            f"{wintun_dll.name} ({wintun_dll.stat().st_size // 1024} KiB, sha256={sha256_file(wintun_dll)[:12]})"
        )

    return 0


if __name__ == "__main__":
    sys.exit(main())
