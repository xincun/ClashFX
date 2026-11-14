#!/usr/bin/env python3
"""Package macos/dist/ClashFX.app into a compressed UDZO ClashFX.dmg."""
from __future__ import annotations

import argparse
from pathlib import Path
import shutil
import subprocess
import tempfile

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_APP = REPO_ROOT / "macos/dist/ClashFX.app"
DEFAULT_DMG = REPO_ROOT / "macos/dist/ClashFX.dmg"


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--app", type=Path, default=DEFAULT_APP)
    parser.add_argument("--out", type=Path, default=DEFAULT_DMG)
    args = parser.parse_args()

    if not args.app.exists():
        raise SystemExit(f"App bundle not found: {args.app}")
    args.out.parent.mkdir(parents=True, exist_ok=True)
    if args.out.exists():
        args.out.unlink()

    with tempfile.TemporaryDirectory(prefix="clashfx-dmg-") as tmp:
        stage = Path(tmp) / "dmg-root"
        stage.mkdir(parents=True, exist_ok=True)
        subprocess.run(["ditto", str(args.app), str(stage / "ClashFX.app")], check=True, timeout=60)
        (stage / "Applications").symlink_to("/Applications")

        subprocess.run(
            [
                "hdiutil",
                "create",
                "-volname",
                "ClashFX",
                "-srcfolder",
                str(stage),
                "-ov",
                "-format",
                "UDZO",
                "-imagekey",
                "zlib-level=6",
                str(args.out),
            ],
            check=True,
            timeout=180,
        )

    subprocess.run(["hdiutil", "verify", str(args.out)], check=True, timeout=120)
    print(f"[OK] Packaged & verified DMG -> {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
