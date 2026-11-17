---
name: win-vm-verify
description: >-
  Build and verify ClashFX Windows Qt6 + WebView2 UI inside the Parallels Windows 11 VM.
  Use this skill whenever modifying windows/ui-qt/, shared Web SPA files affecting Windows,
  or Windows daemon/service integration.
---

# Windows 11 VM Build & Isolated Verification

## Parallels VM Rules

- Use `parallels_vm_exec` on `vm_id: "Windows 11"` with the **default logged-in user** (never `elevated: true` when reading `C:\Mac\...`).
- Copy scripts or build inputs from `C:\Mac\...` to local `C:\Temp\...` first, and guard every spawned process with a hard `.WaitForExit(...)` timeout.

## 1. Fast Single-Arch Build (During Iteration)

Only build `arm64` during normal iteration (half the compile time of dual-arch):

1. If `core-engine/` changed, cross-compile `arm64` daemon/service on macOS:
   ```bash
   python3 windows/scripts/build-daemon.py --arch arm64
   ```
2. Build `arm64` Qt6 + WebView2 UI in the VM:
   ```powershell
   powershell -NoProfile -ExecutionPolicy Bypass -File "C:\Mac\Home\Desktop\workspace\github\ClashFX\windows\scripts\build-ui.ps1" -TargetArch arm64
   ```

## 2. One-Command Isolated VM Smoke Check

Run [smoke-isolated-vm.ps1](./scripts/smoke-isolated-vm.ps1) inside `C:\Temp` using non-colliding pipes/ports (`17892`/`19192`) so it never disturbs any running ClashFX instance:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "New-Item -ItemType Directory -Force -Path C:\Temp | Out-Null; Copy-Item -Force 'C:\Mac\Home\Desktop\workspace\github\ClashFX\.agents\skills\win-vm-verify\scripts\smoke-isolated-vm.ps1' 'C:\Temp\smoke-isolated-vm.ps1'; try { & 'C:\Temp\smoke-isolated-vm.ps1' -Arch arm64 } finally { Remove-Item -Force 'C:\Temp\smoke-isolated-vm.ps1' -ErrorAction SilentlyContinue }"
```

Success output: `[OK] windows-arm64 isolated VM smoke passed (<N> DOM steps)` with exit code `0`.

## 3. Full Dual-Arch Release Verification (Final Sign-Off Only)

1. On macOS: `python3 windows/scripts/build-daemon.py`
2. In VM:
   ```powershell
   powershell -NoProfile -ExecutionPolicy Bypass -File "C:\Mac\Home\Desktop\workspace\github\ClashFX\windows\scripts\build-ui.ps1" -TargetArch all
   powershell -NoProfile -ExecutionPolicy Bypass -File "C:\Mac\Home\Desktop\workspace\github\ClashFX\windows\scripts\verify-ui-vm.ps1"
   ```
