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

printf '\nUniversal macOS app: %s/macos/dist/ClashFX.app\n' "$CLASHFX_REPO_DIR"
