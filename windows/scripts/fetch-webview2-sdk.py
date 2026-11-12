#!/usr/bin/env python3
"""Fetch and verify official Microsoft.Web.WebView2 NuGet SDK for C++ builds.

Extracts:
  windows/build/.cache/webview2/build/native/include/WebView2.h
  windows/build/.cache/webview2/build/native/arm64/WebView2LoaderStatic.lib
  windows/build/.cache/webview2/build/native/x64/WebView2LoaderStatic.lib
"""

from __future__ import annotations

import hashlib
import pathlib
import urllib.request
import zipfile

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
CACHE_DIR = REPO_ROOT / "windows" / "build" / ".cache"
WEBVIEW2_NUPKG_URL = "https://www.nuget.org/api/v2/package/Microsoft.Web.WebView2/1.0.2903.40"
WEBVIEW2_NUPKG_SHA256 = "ef128016dd1e51c59178c827ed5b8aa3322c57afa8675d930f8109505542ad74"
EXTRACT_ROOT = CACHE_DIR / "webview2"


def sha256_file(path: pathlib.Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def ensure_webview2_sdk() -> pathlib.Path:
    CACHE_DIR.mkdir(parents=True, exist_ok=True)
    nupkg = CACHE_DIR / "Microsoft.Web.WebView2.1.0.2903.40.nupkg"
    if not nupkg.is_file() or sha256_file(nupkg) != WEBVIEW2_NUPKG_SHA256:
        tmp = nupkg.with_suffix(".nupkg.tmp")
        urllib.request.urlretrieve(WEBVIEW2_NUPKG_URL, tmp)
        actual = sha256_file(tmp)
        if actual != WEBVIEW2_NUPKG_SHA256:
            tmp.unlink(missing_ok=True)
            raise RuntimeError(
                f"WebView2 SDK SHA256 mismatch: got {actual}, expected {WEBVIEW2_NUPKG_SHA256}"
            )
        tmp.replace(nupkg)

    header = EXTRACT_ROOT / "build" / "native" / "include" / "WebView2.h"
    arm64_lib = EXTRACT_ROOT / "build" / "native" / "arm64" / "WebView2LoaderStatic.lib"
    x64_lib = EXTRACT_ROOT / "build" / "native" / "x64" / "WebView2LoaderStatic.lib"
    if header.is_file() and arm64_lib.is_file() and x64_lib.is_file():
        return EXTRACT_ROOT

    EXTRACT_ROOT.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(nupkg, "r") as zf:
        for member in zf.namelist():
            if member.startswith("build/native/") and not member.endswith("/"):
                zf.extract(member, EXTRACT_ROOT)
    return EXTRACT_ROOT


if __name__ == "__main__":
    sdk_dir = ensure_webview2_sdk()
    print(f"[ok] WebView2 SDK ready at {sdk_dir}")
