#!/usr/bin/env python3
"""Ad-hoc sign copied frameworks inside-out, then the built local app.

Embedding can invalidate the resource seals of pre-signed Sparkle bundles.
This only signs the build artifact; it does not change update settings, use
certificates/keychain items, or modify the installed app or helper.
"""
from pathlib import Path
import subprocess
import sys

app = Path(sys.argv[1]).resolve(strict=True)
if app.suffix != '.app' or not (app / 'Contents/Info.plist').is_file():
    raise SystemExit('Expected the built app bundle')


def sign(path):
    subprocess.run(['codesign', '--force', '--sign', '-', '--preserve-metadata=identifier,entitlements', str(path)], check=True)


frameworks = app / 'Contents/Frameworks'
if frameworks.is_dir():
    standalone = {p.resolve() for p in frameworks.rglob('Autoupdate') if p.is_file()}
    bundles = {p.resolve() for p in frameworks.rglob('*') if p.is_dir() and p.suffix in ['.framework', '.app', '.xpc']}
    for path in sorted(standalone, key=lambda p: len(p.parts), reverse=True):
        if not path.is_relative_to(app):
            raise SystemExit('Refusing to sign a path outside the built app')
        sign(path)
    for path in sorted(bundles, key=lambda p: len(p.parts), reverse=True):
        if not path.is_relative_to(app):
            raise SystemExit('Refusing to sign a path outside the built app')
        sign(path)
sign(app)
subprocess.run(['codesign', '--verify', '--deep', '--strict', str(app)], check=True)
print('Local app signature verified')
