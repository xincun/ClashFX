#!/usr/bin/env python3
"""Stage Universal macOS ClashFX.app bundle with shared resources & Web SPA."""
from __future__ import annotations

import argparse
from pathlib import Path
import plistlib
import shutil
import subprocess

REPO_ROOT = Path(__file__).resolve().parents[2]
UNIVERSAL_BIN = REPO_ROOT / "macos/build/bin/universal"
INFO_PLIST_SRC = REPO_ROOT / "macos/ui-macos/Info.plist"
RESOURCES_SRC = REPO_ROOT / "macos/resources"
WEB_UI_SRC = REPO_ROOT / "windows/ui-qt/web"
DEFAULT_APP_OUT = REPO_ROOT / "macos/dist/ClashFX.app"


def sign_binary(path: Path) -> None:
    subprocess.run(
        ["codesign", "--force", "--sign", "-", "--timestamp=none", str(path)],
        check=True,
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", default="")
    parser.add_argument("--build-number", default="")
    parser.add_argument("--out", type=Path, default=DEFAULT_APP_OUT)
    args = parser.parse_args()

    app_dir: Path = args.out
    contents_dir = app_dir / "Contents"
    macos_dir = contents_dir / "MacOS"
    res_dir = contents_dir / "Resources"

    if app_dir.exists():
        shutil.rmtree(app_dir)
    macos_dir.mkdir(parents=True, exist_ok=True)
    res_dir.mkdir(parents=True, exist_ok=True)

    for binary in ("ClashFX", "clashfx-daemon", "clashfx-service", "mihomo_core"):
        src = UNIVERSAL_BIN / binary
        if not src.exists():
            raise SystemExit(f"Missing built binary: {src}")
        dst = macos_dir / binary
        shutil.copy2(src, dst)
        dst.chmod(0o755)

    with INFO_PLIST_SRC.open("rb") as f:
        info = plistlib.load(f)
    if args.version:
        info["CFBundleShortVersionString"] = args.version
    if args.build_number:
        info["CFBundleVersion"] = args.build_number
    with (contents_dir / "Info.plist").open("wb") as f:
        plistlib.dump(info, f)

    (contents_dir / "PkgInfo").write_text("APPL????", encoding="ascii")

    for entry in RESOURCES_SRC.iterdir():
        dst = res_dir / entry.name
        if entry.is_dir():
            shutil.copytree(entry, dst, dirs_exist_ok=True)
        else:
            shutil.copy2(entry, dst)

    shutil.copytree(WEB_UI_SRC, res_dir / "web", dirs_exist_ok=True)

    # Sign inside-out so codesign --verify --deep --strict passes cleanly.
    for helper in ("mihomo_core", "clashfx-service", "clashfx-daemon"):
        sign_binary(macos_dir / helper)
    sign_binary(app_dir)

    subprocess.run(["codesign", "--verify", "--deep", "--strict", str(app_dir)], check=True)
    print(f"[OK] Staged & signed Universal ClashFX.app -> {app_dir}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
