# ClashFX Agent Development Guide

## Commands (Prefer Fast Checks During Iteration)

- **`make check-fast`** (or `make check-fast PKG=./internal/profile`): daily inner-loop check (<10s) — runs `node --check` on Web SPA, isolated `go test` without `-race`, and host-arch `swiftc -typecheck`. Run this after edits instead of full Universal/dual-arch builds.
- **`make smoke-macos`**: builds & stages Universal `macos/dist/ClashFX.app`, checks binary compatibility floor, and runs an isolated `--smoke-test-ms` DOM + daemon test in `/tmp`.
- **`make build-win-daemon`**: cross-compiles `windows/{arm64,amd64}` `clashfx-daemon.exe` and `clashfx-service.exe`.
- **`make verify-release`**: full `-race` Go suite + macOS Universal app, compatibility gates, and dashboard bundle check (run once before final delivery/release, not after every small edit).

## 4-Layer Architecture Map

- `core-engine/`: unified Go backend (`cmd/clashfx-daemon` JSON-RPC over Unix Socket / Named Pipe; `cmd/clashfx-service` privileged `mihomo_core` supervisor; `internal/profile/store.go` persisted `settings.json`).
- `windows/ui-qt/web/`: **shared Control Center Web SPA for BOTH macOS and Windows** (`macos/scripts/stage-app.py` copies it to `ClashFX.app/Contents/Resources/web`; `windows/scripts/build-ui.ps1` copies it to `windows/dist/windows-<arch>/resources/ui`). Changes here affect both platforms.
- `macos/ui-macos/Sources/`: Swift AppKit + WKWebView + Carbon host (`AppDelegate.swift`, `TrayController.swift`, `ConsoleWindowController.swift`, `HotkeyManager.swift`).
- `windows/ui-qt/src/`: C++17 Qt 6.8 + Edge WebView2 host (`main.cpp`, `TrayController.{h,cpp}`, `ConsoleWindow.{h,cpp}`, `HotkeyManager.{h,cpp}`, `WebView2Widget.{h,cpp}`).

## Hard Pitfalls & Safety Guards

- **Search**: Use `git grep` instead of bare `grep -rn` (`rg` may not be installed, and bare `grep -rn` scans binary bundles under `macos/dist` and `windows/dist`).
- **Go test env guards**: Always run `go test` with `CLASHFX_DISABLE_OS_SYSPROXY=1 CLASHFX_DISABLE_LAUNCH_AGENT=1 CLASHFX_DISABLE_LEGACY_PLIST_MIGRATION=1 CLASHFX_DISABLE_PRIVILEGED_SERVICE=1` (built into `Makefile` targets) to avoid modifying the host OS proxy or LaunchAgents.
- **Qt6 C++ macro collision**: `QT_NO_KEYWORDS` is not set in `windows/ui-qt/CMakeLists.txt`; never name variables or struct fields `slots`, `signals`, or `emit`.
- **Web SPA focus/blur**: In `windows/ui-qt/web/pages-ops.js`, do not call `renderSettingsPage(root)` (`root.innerHTML = ...`) inside `click` or `input` handlers of focused inputs — detaching a focused element fires synchronous `blur`. Update input DOM state in place and re-render only after the interaction finishes.
- **Smoke test isolation**: When `--smoke-test-ms` is active (`smokeMode`), native hosts must skip registering OS global hotkeys and must never pop blocking modal dialogs (`NSAlert.runModal()` / `QMessageBox`).
- **Parallels Windows VM**: Access `C:\Mac\...` only under the default logged-in user identity (`parallels_vm_exec` without `elevated: true`), copy inputs to local `C:\Temp\...` first, and guard every spawned process with a hard timeout (`.WaitForExit(...)`). Activate skill `win-vm-verify` when building or testing on Windows.

## Execution & Output Discipline

- Coalesce changes within the same file into minimal `edit_file` calls rather than many small hunks.
- Delegate broad read-only codebase exploration or verbose build/VM verification runs to subagents so intermediate logs do not flood the main chat.
