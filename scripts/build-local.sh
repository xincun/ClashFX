#!/bin/bash
set -euo pipefail

CLASHFX_REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$CLASHFX_REPO_DIR"

if [ ! -f Pods/Manifest.lock ] || ! cmp -s Podfile.lock Pods/Manifest.lock; then
    pod install --deployment
fi

(
    cd ClashFX/goClash
    GOTOOLCHAIN="${CLASHFX_GO_TOOLCHAIN:-go1.22.12}" python3 build_clash_universal.py --native
)

python3 - <<'PY'
from pathlib import Path
import gzip
import shutil

destination = Path('ClashFX/Resources/Country.mmdb.gz')
if not destination.exists():
    installed = Path('/Applications/ClashFX.app/Contents/Resources/Country.mmdb.gz')
    cached = Path.home() / '.config/clashfx/Country.mmdb'
    if installed.exists():
        shutil.copyfile(installed, destination)
    elif cached.exists():
        with gzip.open(destination, 'wb') as output:
            output.write(cached.read_bytes())
    else:
        raise SystemExit('Prepare Country.mmdb.gz as described in install_dependency.sh before building.')
PY

xcodebuild -workspace ClashFX.xcworkspace -scheme ClashFX -configuration Release \
    -derivedDataPath build_output/DerivedData \
    -destination "platform=macOS,arch=$(uname -m)" -jobs 8 build \
    CLASHFX_SKIP_FORMAT=1 MACOSX_DEPLOYMENT_TARGET="${CLASHFX_LOCAL_MIN_MACOS:-12.0}" \
    CODE_SIGN_IDENTITY=- CODE_SIGN_STYLE=Manual DEVELOPMENT_TEAM= \
    CODE_SIGNING_REQUIRED=NO ENABLE_HARDENED_RUNTIME=NO ONLY_ACTIVE_ARCH=YES

python3 scripts/sign-local-app.py build_output/DerivedData/Build/Products/Release/ClashFX.app
printf '\nLocal app: %s/build_output/DerivedData/Build/Products/Release/ClashFX.app\n' "$CLASHFX_REPO_DIR"
