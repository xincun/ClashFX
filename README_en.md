<h1 align="center">ClashFX</h1>

<h4 align="center">Cross-platform macOS & Windows rule-based proxy and Enhanced Mode (TUN) client — powered by upstream mihomo core and a unified Go engine.</h4>

<div align="center">

[简体中文](README.md) | [English](README_en.md)

</div>

---

ClashFX is built on a **unified Go control engine + shared Control Center Web SPA + lightweight native desktop hosts** architecture: a single Go daemon and privileged background service supervise an unmodified upstream [mihomo](https://github.com/MetaCubeX/mihomo) core, while native macOS (Swift / AppKit / WKWebView) and Windows (C++17 / Qt 6.8 / WebView2) hosts deliver a consistent UI, system tray, and OS integration across both platforms.

## ✨ Key Features

- **Ready Out of the Box — Add One Subscription and Go**:
  - Installers deploy dependencies, GeoIP/GeoSite databases, and the privileged background service (`LaunchDaemon` on macOS, `ClashFXService` + `wintun.dll` + WebView2 on Windows) during setup — toggling Enhanced Mode (TUN) works with **zero recurring elevation prompts**.
  - Ships with sensible defaults enabled on first launch: **Rule mode**, **System Proxy**, **Enhanced Mode (TUN, `mixed` stack)**, **LAN & loopback bypass**, **CN direct**, and **built-in Chinese Desktop Apps Direct** (`PROCESS-NAME` rules for WeChat, QQ, DingTalk, Feishu, NetEase Cloud Music, WPS, Baidu Netdisk, etc., bundled offline for both macOS and Windows).
- **Consistent Native + Web Control Center Across macOS & Windows**:
  - Unified 7-section Control Center (**MetaCubeXD `v1.266.1`** console, **Subscriptions & Profiles**, **Routing & Rules Studio**, **AI Proxy Lock**, **Multi-Node Services**, **Node Diagnostics**, and **Preferences**) alongside native system tray menus and customizable global hotkeys.
  - Supports **Obsidian Dark** / **Alabaster Light** themes and live **简体中文 / English** switching.
- **Fail-Closed AI Proxy Lock**:
  - Pins traffic for **Claude**, **Codex**, and **Antigravity (`agy`)** desktop apps, CLIs, and API endpoints to a specific node with hard `REJECT` fallback rules so AI traffic never leaks to `DIRECT` or unintended proxies. See [docs/AI-PROXY-LOCK.md](docs/AI-PROXY-LOCK.md).
- **Multi-Node Fixed Ports & Health Watchdog**:
  - Assigns persistent `127.0.0.1:21000+` mixed HTTP + SOCKS5 listeners per proxy node, supports sequential 20 MB Cloudflare speed tests, runs 60s connectivity health probes, and auto-refreshes managed subscriptions through a `DIRECT`-pinned listener on consecutive failures. See [docs/NODE-SERVICES.md](docs/NODE-SERVICES.md).
- **Unmodified Upstream Core & Transactional Safety**:
  - Runs 100% unmodified upstream `github.com/metacubex/mihomo` (`-tags with_gvisor`) with in-app zero-prompt core updates.
  - Profile reloads, Mixins, and synthetic rules pass preflight validation before atomic switch-over, backed by `.runtime-last-good.yaml` automatic rollback and Wi-Fi/network auto-suspend.
- **Automated CI Releases & In-App Updates**:
  - Supports **Stable** and **Lab** update channels; installed clients automatically check [GitHub Releases](https://github.com/xincun/ClashFX/releases) and select the matching OS/architecture installer.

---

## 📥 Download & Installation

Download prebuilt packages from [Releases](https://github.com/xincun/ClashFX/releases):

| Platform & Architecture | Recommended Installer | Notes / Portable |
| :--- | :--- | :--- |
| **macOS 12.0+** (Apple Silicon & Intel Universal) | `ClashFX.dmg` / `ClashFX.pkg` | Universal binary; installer configures privileged TUN service automatically |
| **Windows 10/11 x64 (`amd64`)** | `ClashFX-Setup-amd64.exe` | Registers `ClashFXService` & `clash://`/`clashfx://` schemes; portable zip: `ClashFX-Portable-amd64.zip` |
| **Windows 10/11 ARM64 (`arm64`)** | `ClashFX-Setup-arm64.exe` | Native ARM64 installer (no emulation); portable zip: `ClashFX-Portable-arm64.zip` |

> After installation, launch ClashFX and paste a subscription URL (or click a `clash://` / `clashfx://` link) in **Subscriptions & Profiles** to start routing traffic.

---

## 🏗️ Architecture & Repository Layout

```text
ClashFX/
├── core-engine/            # Unified cross-platform Go backend (daemon + service + tests)
│   ├── cmd/clashfx-daemon/ # User-space IPC control daemon (Unix Socket / Named Pipe)
│   └── cmd/clashfx-service/# Privileged mihomo_core supervisor (LaunchDaemon / Windows Service)
├── macos/                  # macOS host: pure Swift AppKit + WKWebView + packaging scripts
│   ├── ui-macos/           # Tray menu, global hotkeys, WKWebView Control Center host
│   ├── resources/          # Icons, bundled MetaCubeXD dashboard, GeoIP/GeoSite archives
│   └── scripts/            # build-core.py / build-daemon.py / build-ui.py / stage-app.py / package-{pkg,dmg}.py
├── windows/                # Windows host: Qt 6.8 + Edge WebView2 + shared Web SPA + Inno Setup
│   ├── ui-qt/              # C++17 Qt6 host shell + web/ Control Center SPA (shared with macOS)
│   ├── installer/          # Dual-arch Inno Setup script (clashfx.iss)
│   └── scripts/            # build-core.py / build-daemon.py / build-ui.ps1 / package-release.ps1
├── scripts/                # Local macOS build script (build-local.sh) & compatibility gate checks
├── .github/workflows/      # GitHub Actions CI (ci.yml) & unified release pipeline (release.yml)
└── docs/                   # Architecture & feature guides (see docs/README.md)
```

For runtime lifecycle details, IPC security boundaries, and rollback guarantees, see [docs/README.md](docs/README.md).

---

## ⚙️ Configuration Directories & URL Schemes

- **Default config directory**:
  - macOS: `$HOME/.config/clashfx`
  - Windows: `%APPDATA%\ClashFX`
- **URL schemes**:
  - Import remote subscription: `clashfx://install-config?url=<encoded-url>&name=<name>` or `clash://install-config?url=<encoded-url>&name=<name>`
  - Reload active config: `clash://update-config`

---

## 🔨 Building from Source & Automated Releases

### Run Go Core Engine Tests

```bash
cd core-engine
go test -race -count=1 ./...
```

### Build macOS Universal App, PKG & DMG (`arm64` + `x86_64`)

Prerequisites: macOS 12.0+ toolchain (`swiftc`, `lipo`, `codesign`, `pkgbuild`, `hdiutil`), Go 1.26+, Python 3. No Xcode project or CocoaPods required:

```bash
./scripts/build-local.sh
# Outputs macos/dist/ClashFX.app, macos/dist/ClashFX.pkg, and macos/dist/ClashFX.dmg
```

### Build Windows Dual-Arch Packages (`arm64` & `amd64`)

See [windows/README.md](windows/README.md) for full steps:

1. `python3 windows/scripts/build-core.py`
2. `python3 windows/scripts/build-daemon.py`
3. `python3 windows/scripts/fetch-webview2-sdk.py`
4. On Windows (Visual Studio 2022 + Qt 6.8 + Inno Setup 6): run `windows\scripts\build-ui.ps1` and `windows\scripts\package-release.ps1`

### CI/CD Workflow

Configured in [release.yml](.github/workflows/release.yml) and [ci.yml](.github/workflows/ci.yml):

- Pushing code changes to `release`, pushing a version tag (e.g. `v1.20.0`), or triggering **Run workflow** builds macOS Universal + Windows `amd64` + Windows `arm64` in parallel, verifies tests and artifacts, generates `SHA256SUMS.txt`, and publishes a unified release to [GitHub Releases](https://github.com/xincun/ClashFX/releases).
- Docs-only pushes to `release` automatically skip binary release builds.

---

## 📄 License & Acknowledgments

Licensed under [AGPL-3.0](LICENSE).

Thanks to:
- [MetaCubeX/mihomo](https://github.com/MetaCubeX/mihomo) — Proxy core engine
- [MetaCubeX/metacubexd](https://github.com/MetaCubeX/metacubexd) — Bundled Web dashboard
- [MetaCubeX/meta-rules-dat](https://github.com/MetaCubeX/meta-rules-dat) — GeoIP / GeoSite rule databases
- [WireGuard Wintun](https://www.wintun.net/) — High-performance Layer 3 TUN driver for Windows
