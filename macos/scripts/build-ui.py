#!/usr/bin/env python3
"""Compile native Swift AppKit/WKWebView ClashFX UI into a Universal Mach-O binary."""
from __future__ import annotations

from pathlib import Path
import subprocess

REPO_ROOT = Path(__file__).resolve().parents[2]
SOURCES_DIR = REPO_ROOT / "macos/ui-macos/Sources"
BUILD_BIN = REPO_ROOT / "macos/build/bin"


def main() -> int:
    sdk = subprocess.check_output(["xcrun", "--sdk", "macosx", "--show-sdk-path"], text=True).strip()
    sources = sorted(str(p) for p in SOURCES_DIR.glob("*.swift"))
    if not sources:
        raise SystemExit(f"No Swift sources found in {SOURCES_DIR}")

    universal_dir = BUILD_BIN / "universal"
    universal_dir.mkdir(parents=True, exist_ok=True)

    arch_bins: list[Path] = []
    for arch, target in (("arm64", "arm64-apple-macos12.0"), ("amd64", "x86_64-apple-macos12.0")):
        out_bin = BUILD_BIN / arch / "ClashFX"
        out_bin.parent.mkdir(parents=True, exist_ok=True)
        print(f"[build-ui] Compiling Swift UI ({target}) -> {out_bin}")
        subprocess.run(
            [
                "xcrun",
                "swiftc",
                "-O",
                "-sdk",
                sdk,
                "-target",
                target,
                "-framework",
                "Cocoa",
                "-framework",
                "WebKit",
                "-framework",
                "Carbon",
                *sources,
                "-o",
                str(out_bin),
            ],
            check=True,
        )
        arch_bins.append(out_bin)

    uni_bin = universal_dir / "ClashFX"
    subprocess.run(["xcrun", "lipo", "-create", *[str(p) for p in arch_bins], "-output", str(uni_bin)], check=True)
    uni_bin.chmod(0o755)
    print(f"[OK] Universal ClashFX UI -> {uni_bin}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
