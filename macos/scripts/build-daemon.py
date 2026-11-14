#!/usr/bin/env python3
"""Build Universal (arm64 + x86_64) clashfx-daemon and clashfx-service for macOS."""
from __future__ import annotations

import argparse
import os
from pathlib import Path
import subprocess

REPO_ROOT = Path(__file__).resolve().parents[2]
CORE_ENGINE_DIR = REPO_ROOT / "core-engine"
BUILD_BIN = REPO_ROOT / "macos/build/bin"


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", default="1.19.24")
    args = parser.parse_args()

    universal_dir = BUILD_BIN / "universal"
    universal_dir.mkdir(parents=True, exist_ok=True)
    ldflags = f"-s -w -X github.com/clashfx/core-engine/internal/daemon.AppVersion={args.version}"

    for binary, pkg in (("clashfx-daemon", "./cmd/clashfx-daemon"), ("clashfx-service", "./cmd/clashfx-service")):
        arch_bins: list[Path] = []
        for arch in ("arm64", "amd64"):
            out_bin = BUILD_BIN / arch / binary
            out_bin.parent.mkdir(parents=True, exist_ok=True)
            env = dict(os.environ)
            env.update(
                CGO_ENABLED="0",
                GOOS="darwin",
                GOARCH=arch,
                MACOSX_DEPLOYMENT_TARGET="12.0",
            )
            print(f"[build-daemon] Building {binary} darwin/{arch} -> {out_bin}")
            subprocess.run(
                ["go", "build", "-trimpath", "-ldflags", ldflags, "-o", str(out_bin), pkg],
                cwd=CORE_ENGINE_DIR,
                env=env,
                check=True,
            )
            arch_bins.append(out_bin)

        uni_bin = universal_dir / binary
        subprocess.run(["xcrun", "lipo", "-create", *[str(p) for p in arch_bins], "-output", str(uni_bin)], check=True)
        uni_bin.chmod(0o755)
        print(f"[OK] Universal {binary} -> {uni_bin}")

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
