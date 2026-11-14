#!/usr/bin/env python3
"""Read-only macOS Universal Mach-O & bundle compatibility gates."""
import argparse
from pathlib import Path
import plistlib
import re
import subprocess

MAX_MACOS_MIN = "12.0"


def version(value):
    parts = [int(part) for part in str(value).split(".")]
    return tuple((parts + [0, 0, 0])[:3])


def command(*args):
    return subprocess.check_output(args, text=True).strip()


def verify_sdk():
    sdk = Path(command("xcrun", "--sdk", "macosx", "--show-sdk-path"))
    with (sdk / "SDKSettings.plist").open("rb") as stream:
        target = plistlib.load(stream)["SupportedTargets"]["macosx"]
    minimum = target["MinimumDeploymentTarget"]
    if version(minimum) > version(MAX_MACOS_MIN):
        raise RuntimeError(
            f"SDK minimum is {minimum}; select a toolchain supporting macOS {MAX_MACOS_MIN}."
        )
    print(f"SDK deployment floor: {minimum}; macOS {MAX_MACOS_MIN} target accepted")


def verify_binary(path: Path):
    archs = command("xcrun", "lipo", "-archs", str(path)).split()
    for arch in ("x86_64", "arm64"):
        if arch not in archs:
            raise RuntimeError(f"{path}: missing {arch} slice (found {archs})")
        output = command("xcrun", "otool", "-arch", arch, "-l", str(path))
        if not re.search(r"cmd LC_UUID\b", output):
            raise RuntimeError(f"{path}: {arch} missing LC_UUID required by modern macOS loaders")
        minima = []
        for block in output.split("Load command"):
            if re.search(r"cmd LC_BUILD_VERSION\b", block):
                match = re.search(r"\bminos\s+([\d.]+)", block)
            elif re.search(r"cmd LC_VERSION_MIN_MACOSX\b", block):
                match = re.search(r"\bversion\s+([\d.]+)", block)
            else:
                continue
            if match:
                minima.append(match.group(1))
        if not minima or any(version(value) > version(MAX_MACOS_MIN) for value in minima):
            raise RuntimeError(f"{path}: {arch} minimum {minima or 'unknown'} exceeds {MAX_MACOS_MIN}")
        print(f"{path.name}: {arch} minimum {', '.join(minima)}")


def verify_app(path: Path):
    with (path / "Contents/Info.plist").open("rb") as stream:
        info = plistlib.load(stream)
    minimum = info.get("LSMinimumSystemVersion")
    if minimum is None or version(minimum) > version(MAX_MACOS_MIN):
        raise RuntimeError(f"App LSMinimumSystemVersion is {minimum!r}, expected <= {MAX_MACOS_MIN}")

    exe_name = info.get("CFBundleExecutable", "ClashFX")
    required_binaries = [exe_name, "clashfx-daemon", "clashfx-service", "mihomo_core"]
    for name in required_binaries:
        bin_path = path / "Contents/MacOS" / name
        if not bin_path.is_file():
            raise RuntimeError(f"Missing bundled executable: {bin_path}")
        verify_binary(bin_path)

    print("Universal macOS app, daemon, service, and mihomo_core architecture/minimum-version gates passed")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--sdk", action="store_true")
    parser.add_argument("--app", type=Path)
    args = parser.parse_args()
    if not args.sdk and args.app is None:
        parser.error("specify --sdk or --app")
    if args.sdk:
        verify_sdk()
    if args.app is not None:
        verify_app(args.app)
