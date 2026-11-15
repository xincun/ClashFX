#!/bin/bash
set -euo pipefail

CLASHFX_REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$CLASHFX_REPO_DIR"

python3 macos/scripts/build-core.py
python3 macos/scripts/build-daemon.py
python3 macos/scripts/build-ui.py
python3 macos/scripts/stage-app.py

python3 scripts/verify-release-compatibility.py --app macos/dist/ClashFX.app
bash scripts/verify-dashboard-compatibility-bundle.sh macos/dist/ClashFX.app
python3 macos/scripts/package-pkg.py
python3 macos/scripts/package-dmg.py

printf '\nUniversal macOS app: %s/macos/dist/ClashFX.app\n' "$CLASHFX_REPO_DIR"
printf 'Universal macOS pkg: %s/macos/dist/ClashFX.pkg\n' "$CLASHFX_REPO_DIR"
printf 'Universal macOS dmg: %s/macos/dist/ClashFX.dmg\n' "$CLASHFX_REPO_DIR"
