#!/usr/bin/env python3
"""Build dual-arch (arm64 & amd64) clashfx-service.exe and clashfx-daemon.exe."""

import os
import pathlib
import shutil
import struct
import subprocess
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
CORE_ENGINE_DIR = REPO_ROOT / "core-engine"
BIN_ROOT = REPO_ROOT / "windows" / "build" / "bin"
DIST_ROOT = REPO_ROOT / "windows" / "dist"

TARGETS = [
    ("arm64", 0xAA64),
    ("amd64", 0x8664),
]

BINARIES = [
    ("clashfx-service.exe", "./cmd/clashfx-service"),
    ("clashfx-daemon.exe", "./cmd/clashfx-daemon"),
]


def verify_pe_machine(path: pathlib.Path, expected_machine: int) -> None:
    data = path.read_bytes()
    if data[:2] != b"MZ":
        raise RuntimeError(f"{path} is missing MZ header")
    pe_offset = struct.unpack_from("<I", data, 0x3C)[0]
    if data[pe_offset : pe_offset + 4] != b"PE\x00\x00":
        raise RuntimeError(f"{path} is missing PE signature")
    machine = struct.unpack_from("<H", data, pe_offset + 4)[0]
    if machine != expected_machine:
        raise RuntimeError(f"{path} PE machine=0x{machine:04X}, expected 0x{expected_machine:04X}")
    if path.suffix.lower() == ".exe":
        opt_magic = struct.unpack_from("<H", data, pe_offset + 24)[0]
        if opt_magic != 0x020B:
            raise RuntimeError(f"{path} unexpected OptionalHeader magic=0x{opt_magic:04X}, expected 0x020B")
        subsystem = struct.unpack_from("<H", data, pe_offset + 92)[0]
        if subsystem != 2:
            raise RuntimeError(f"{path} PE Subsystem={subsystem}, expected 2 (WINDOWS_GUI)")


def main() -> int:
    for arch, expected_machine in TARGETS:
        out_dir = BIN_ROOT / arch
        out_dir.mkdir(parents=True, exist_ok=True)
        env = os.environ.copy()
        env["GOOS"] = "windows"
        env["GOARCH"] = arch
        env["CGO_ENABLED"] = "0"
        for exe_name, pkg in BINARIES:
            out_path = out_dir / exe_name
            subprocess.check_call(
                ["go", "build", "-trimpath", "-ldflags=-H=windowsgui -s -w", "-o", str(out_path), pkg],
                cwd=str(CORE_ENGINE_DIR),
                env=env,
            )
            verify_pe_machine(out_path, expected_machine)
            dist_stage = DIST_ROOT / f"windows-{arch}"
            if dist_stage.is_dir():
                shutil.copy2(out_path, dist_stage / exe_name)
            kib = out_path.stat().st_size // 1024
            print(f"  [OK] windows/{arch}: {exe_name} ({kib} KiB)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
