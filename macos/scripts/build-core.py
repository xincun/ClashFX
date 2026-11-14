#!/usr/bin/env python3
"""Build Universal (arm64 + x86_64) stock upstream Mihomo core for macOS."""
from __future__ import annotations

import os
from pathlib import Path
import subprocess

REPO_ROOT = Path(__file__).resolve().parents[2]
CORE_ENGINE_DIR = REPO_ROOT / "core-engine"
BUILD_BIN = REPO_ROOT / "macos/build/bin"


def resolve_mihomo() -> tuple[Path, str]:
    out = subprocess.check_output(
        ["go", "list", "-m", "-f", "{{.Dir}}\t{{.Version}}", "github.com/metacubex/mihomo"],
        cwd=CORE_ENGINE_DIR,
        text=True,
    ).strip()
    mod_dir, ver = out.split("\t", 1)
    return Path(mod_dir), ver


def main() -> int:
    mihomo_dir, ver = resolve_mihomo()
    print(f"[build-core] mihomo {ver} at {mihomo_dir}")

    universal_dir = BUILD_BIN / "universal"
    universal_dir.mkdir(parents=True, exist_ok=True)
    ldflags = f"-s -w -X github.com/metacubex/mihomo/constant.Version={ver}"

    arch_bins: list[Path] = []
    for arch in ("arm64", "amd64"):
        out_bin = BUILD_BIN / arch / "mihomo_core"
        out_bin.parent.mkdir(parents=True, exist_ok=True)
        env = dict(os.environ)
        env.update(
            CGO_ENABLED="0",
            GOOS="darwin",
            GOARCH=arch,
            MACOSX_DEPLOYMENT_TARGET="12.0",
        )
        print(f"[build-core] Building darwin/{arch} -> {out_bin}")
        subprocess.run(
            ["go", "build", "-trimpath", "-tags", "with_gvisor", "-ldflags", ldflags, "-o", str(out_bin), "."],
            cwd=mihomo_dir,
            env=env,
            check=True,
        )
        arch_bins.append(out_bin)

    uni_bin = universal_dir / "mihomo_core"
    subprocess.run(["xcrun", "lipo", "-create", *[str(p) for p in arch_bins], "-output", str(uni_bin)], check=True)
    uni_bin.chmod(0o755)
    print(f"[OK] Universal mihomo_core -> {uni_bin}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
