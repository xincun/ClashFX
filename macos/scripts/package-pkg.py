#!/usr/bin/env python3
"""Package macos/dist/ClashFX.app into a non-relocatable signed/flat macOS ClashFX.pkg."""
from __future__ import annotations

import argparse
from html import escape
import os
from pathlib import Path
import plistlib
import subprocess
import tempfile
import xml.etree.ElementTree as ET

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_APP = REPO_ROOT / "macos/dist/ClashFX.app"
DEFAULT_PKG = REPO_ROOT / "macos/dist/ClashFX.pkg"

PREINSTALL_SH = """#!/bin/bash
set -u

MARKER="/var/run/clashfx-gui-was-running"
rm -f "$MARKER"

CONSOLE_USER=$(stat -f '%Su' /dev/console 2>/dev/null || true)
CONSOLE_UID=$(id -u "$CONSOLE_USER" 2>/dev/null || true)

if pgrep -f '/Applications/ClashFX.app/Contents/MacOS/ClashFX' >/dev/null 2>&1; then
    touch "$MARKER"
    chmod 600 "$MARKER"
    if [ -n "$CONSOLE_USER" ] && [ "$CONSOLE_USER" != "root" ] && [ -n "$CONSOLE_UID" ]; then
        launchctl asuser "$CONSOLE_UID" sudo -u "$CONSOLE_USER" "/Applications/ClashFX.app/Contents/MacOS/ClashFX" --quit >/dev/null 2>&1 || true
    fi
    for _ in $(seq 1 15); do
        if ! pgrep -f '/Applications/ClashFX.app/Contents/MacOS/ClashFX' >/dev/null 2>&1; then
            break
        fi
        sleep 0.2
    done
fi

if pgrep -f '/Applications/ClashFX.app/Contents/MacOS/(ClashFX|clashfx-daemon)' >/dev/null 2>&1; then
    pkill -TERM -f '/Applications/ClashFX.app/Contents/MacOS/(ClashFX|clashfx-daemon)' >/dev/null 2>&1 || true
    for _ in $(seq 1 10); do
        if ! pgrep -f '/Applications/ClashFX.app/Contents/MacOS/(ClashFX|clashfx-daemon)' >/dev/null 2>&1; then
            break
        fi
        sleep 0.2
    done
    if pgrep -f '/Applications/ClashFX.app/Contents/MacOS/(ClashFX|clashfx-daemon)' >/dev/null 2>&1; then
        pkill -KILL -f '/Applications/ClashFX.app/Contents/MacOS/(ClashFX|clashfx-daemon)' >/dev/null 2>&1 || true
        sleep 0.2
    fi
fi

exit 0
"""

POSTINSTALL_SH = """#!/bin/bash
set -eu

APP_PATH="/Applications/ClashFX.app"
MARKER="/var/run/clashfx-gui-was-running"

/usr/bin/xattr -cr "$APP_PATH" 2>/dev/null || true
/usr/sbin/chown -R root:wheel "$APP_PATH"
/bin/chmod -R a+rX "$APP_PATH"

"$APP_PATH/Contents/MacOS/clashfx-service" install --source-dir "$APP_PATH/Contents/MacOS"

WAS_RUNNING=0
if [ -f "$MARKER" ]; then
    WAS_RUNNING=1
fi
rm -f "$MARKER"

if [ "$WAS_RUNNING" = "1" ] || [ -z "${COMMAND_LINE_INSTALL:-}" ]; then
    CONSOLE_USER=$(stat -f '%Su' /dev/console 2>/dev/null || true)
    CONSOLE_UID=$(id -u "$CONSOLE_USER" 2>/dev/null || true)
    if [ -n "$CONSOLE_USER" ] && [ "$CONSOLE_USER" != "root" ] && [ -n "$CONSOLE_UID" ]; then
        launchctl asuser "$CONSOLE_UID" sudo -u "$CONSOLE_USER" open -a "$APP_PATH" >/dev/null 2>&1 || true
    fi
fi

exit 0
"""


def read_app_version(app_path: Path) -> str:
    info_plist = app_path / "Contents/Info.plist"
    with info_plist.open("rb") as f:
        info = plistlib.load(f)
    ver = str(info.get("CFBundleShortVersionString") or "").strip()
    if not ver:
        raise SystemExit(f"Missing CFBundleShortVersionString in {info_plist}")
    return ver


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--app", type=Path, default=DEFAULT_APP)
    parser.add_argument("--out", type=Path, default=DEFAULT_PKG)
    parser.add_argument("--version", default="")
    args = parser.parse_args()

    if not args.app.exists():
        raise SystemExit(f"App bundle not found: {args.app}")
    version = args.version.strip() or read_app_version(args.app)

    args.out.parent.mkdir(parents=True, exist_ok=True)
    if args.out.exists():
        args.out.unlink()

    with tempfile.TemporaryDirectory(prefix="clashfx-pkg-") as tmp_str:
        tmp = Path(tmp_str)
        payload_root = tmp / "root"
        app_dst = payload_root / "Applications/ClashFX.app"
        app_dst.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(["ditto", str(args.app), str(app_dst)], check=True, timeout=60)

        comp_plist = tmp / "component.plist"
        subprocess.run(
            ["pkgbuild", "--analyze", "--root", str(payload_root), str(comp_plist)],
            check=True,
            timeout=60,
        )
        with comp_plist.open("rb") as f:
            components = plistlib.load(f)
        if not components:
            raise RuntimeError("pkgbuild --analyze produced no bundle components")
        for comp in components:
            comp["BundleIsRelocatable"] = False
            comp["BundleHasStrictIdentifier"] = True
            comp["BundleIsVersionChecked"] = False
            comp["BundleOverwriteAction"] = "upgrade"
        with comp_plist.open("wb") as f:
            plistlib.dump(components, f)

        scripts_dir = tmp / "scripts"
        scripts_dir.mkdir(parents=True, exist_ok=True)
        preinstall_path = scripts_dir / "preinstall"
        postinstall_path = scripts_dir / "postinstall"
        preinstall_path.write_text(PREINSTALL_SH, encoding="utf-8")
        postinstall_path.write_text(POSTINSTALL_SH, encoding="utf-8")
        preinstall_path.chmod(0o755)
        postinstall_path.chmod(0o755)

        subprocess.run(["bash", "-n", str(preinstall_path)], check=True)
        subprocess.run(["bash", "-n", str(postinstall_path)], check=True)

        comp_pkg = tmp / "ClashFX-component.pkg"
        subprocess.run(
            [
                "pkgbuild",
                "--root",
                str(payload_root),
                "--component-plist",
                str(comp_plist),
                "--scripts",
                str(scripts_dir),
                "--identifier",
                "com.clashfx.pkg.app",
                "--version",
                version,
                "--install-location",
                "/",
                str(comp_pkg),
            ],
            check=True,
            timeout=180,
        )

        dist_xml = tmp / "Distribution.xml"
        dist_xml.write_text(
            f"""<?xml version="1.0" encoding="utf-8"?>
<installer-gui-script minSpecVersion="2">
    <title>ClashFX</title>
    <options customize="never" require-scripts="true" hostArchitectures="x86_64,arm64"/>
    <domains enable_anywhere="false" enable_currentUserHome="false" enable_localSystem="true"/>
    <volume-check>
        <allowed-os-versions>
            <os-version min="12.0"/>
        </allowed-os-versions>
    </volume-check>
    <choices-outline>
        <line choice="com.clashfx.pkg.choice"/>
    </choices-outline>
    <choice id="com.clashfx.pkg.choice" visible="false">
        <pkg-ref id="com.clashfx.pkg.app"/>
    </choice>
    <pkg-ref id="com.clashfx.pkg.app" version="{escape(version, quote=True)}" onConclusion="none">ClashFX-component.pkg</pkg-ref>
</installer-gui-script>
""",
            encoding="utf-8",
        )

        subprocess.run(
            [
                "productbuild",
                "--distribution",
                str(dist_xml),
                "--package-path",
                str(tmp),
                str(args.out),
            ],
            check=True,
            timeout=180,
        )

        expanded = tmp / "expanded"
        subprocess.run(
            ["pkgutil", "--expand-full", str(args.out), str(expanded)],
            check=True,
            timeout=120,
        )
        comp_expanded = expanded / "ClashFX-component.pkg"
        pkg_info = comp_expanded / "PackageInfo"
        if not pkg_info.is_file():
            raise RuntimeError(f"Missing PackageInfo in expanded pkg: {pkg_info}")
        pkg_info_text = pkg_info.read_text(encoding="utf-8", errors="replace")
        pkg_xml = ET.fromstring(pkg_info_text)
        if pkg_xml.attrib.get("relocatable") != "false":
            raise RuntimeError(f"Expected pkg-info relocatable='false', got {pkg_xml.attrib!r}:\n{pkg_info_text}")
        for reloc in pkg_xml.findall(".//relocate"):
            if len(list(reloc)) > 0:
                raise RuntimeError(f"Expected non-relocatable <relocate/> with zero bundle entries:\n{pkg_info_text}")

        for script_name in ("preinstall", "postinstall"):
            script_file = comp_expanded / "Scripts" / script_name
            if not script_file.is_file() or not os.access(script_file, os.X_OK):
                raise RuntimeError(f"Missing or non-executable script in pkg: {script_file}")

        payload_bin = comp_expanded / "Payload/Applications/ClashFX.app/Contents/MacOS/ClashFX"
        if not payload_bin.is_file():
            raise RuntimeError(f"Missing ClashFX binary in pkg payload: {payload_bin}")

    print(f"[OK] Packaged & verified non-relocatable PKG (v{version}) -> {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
