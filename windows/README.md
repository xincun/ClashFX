# ClashFX for Windows (`arm64` & `amd64`)

Native Windows port of **ClashFX** supporting both **`windows-arm64`** and **`windows-amd64`**:

- **UI (`clashfx-ui.exe`, `windows/ui-qt/`)**: C++17 **Qt 6.8** (`Qt6Widgets`, `Qt6Network`, `Qt6Svg`) + Microsoft Edge **WebView2** (`ICoreWebView2Controller`) hosting the bundled **MetaCubeXD** console, system tray menu, global hotkeys, and native management dialogs (AI Proxy Lock, Multi-Node Fixed Ports & 20MB Speed Test, Managed Subscriptions, Single-Node Probe Diagnostics, Settings).
- **Control Daemon (`clashfx-daemon.exe`, `core-engine/cmd/clashfx-daemon/`)**: Pure Go user-space IPC daemon over DACL-protected Named Pipe `\\.\pipe\ClashFXDaemon-<username>`, managing `%APPDATA%\ClashFX` (or custom config directory), in-process share-link subscription conversion, `Profile Mixin`, transactional reload & automatic `.runtime-last-good.yaml` rollback, WinINet System Proxy + crash/wake self-healing, **AI Proxy Lock**, **Multi-Node Fixed Ports (`21000+`, pure 1-port-to-1-node proxy bindings without sub-rules) + 60s 204 health watchdog + `DIRECT` subscription refresh**, Chinese apps bypass, and location-permission-free network auto-suspend.
- **Privileged TUN Service (`clashfx-service.exe`, `core-engine/cmd/clashfx-service/`)**: Minimal pure-Go Windows Service (`ClashFXService`) supervising `<service_dir>\mihomo_core.exe` inside a Win32 Job Object (`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`) with standard `-d`/`-f` flags so Enhanced Mode (Wintun TUN) switches with **zero UAC prompts** and supports online core upgrades out of the box.
- **Proxy Core (`mihomo_core.exe` + `wintun.dll`)**: 100% unmodified stock upstream `github.com/metacubex/mihomo` (`-tags with_gvisor`) paired with SHA256/PE-verified official WireGuard `wintun.dll` `0.14.1`.

---

## End-to-End Dual-Architecture Build

1. **Build `mihomo_core.exe` + `wintun.dll` (`arm64` & `amd64`)**:
   ```bash
   python3 windows/scripts/build-core.py
   ```
2. **Build `clashfx-service.exe` + `clashfx-daemon.exe` (`arm64` & `amd64`)**:
   ```bash
   python3 windows/scripts/build-daemon.py
   ```
3. **Fetch official `Microsoft.Web.WebView2` C++ SDK (`WebView2.h` + `WebView2LoaderStatic.lib`)**:
   ```bash
   python3 windows/scripts/fetch-webview2-sdk.py
   ```
4. **Build & deploy Qt6 + WebView2 UI (`windows/dist/windows-arm64` & `windows/dist/windows-amd64`)** (on Windows with Visual Studio 2022 + Qt 6.8 `arm64` & `x64`):
   ```powershell
   powershell -ExecutionPolicy Bypass -File windows\scripts\build-ui.ps1
   ```
5. **Compile Dual-Arch Inno Setup Installers & Portable Archives (`ClashFX-Setup-{arm64,amd64}.exe`, `ClashFX-Portable-{arm64,amd64}.zip`)**:
   ```powershell
   powershell -ExecutionPolicy Bypass -File windows\scripts\package-release.ps1
   ```
