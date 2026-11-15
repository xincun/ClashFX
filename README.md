<h1 align="center">ClashFX</h1>

<h4 align="center">跨平台 macOS & Windows 规则分流与增强模式（TUN）代理桌面客户端 —— 基于原生 Mihomo 内核与统一 Go 控制引擎</h4>

<div align="center">

[简体中文](README.md) | [English](README_en.md)

</div>

---

ClashFX 采用**统一 Go 控制引擎 + 跨平台 Web 控制中心 + 双端轻量原生宿主**架构：后台由统一的 Go 守护进程与特权系统服务驱动 100% 未修改的上游 [mihomo](https://github.com/MetaCubeX/mihomo) 内核，前端由 macOS（Swift / AppKit / WKWebView）与 Windows（C++17 / Qt 6.8 / WebView2）承载一致的现代化控制台与系统托盘体验。

## ✨ 核心特性

- **开箱即用，导入一个订阅即可工作**：
  - 安装包阶段自动部署运行依赖、GEO 数据库与后台特权服务（macOS `LaunchDaemon`、Windows `ClashFXService` + `wintun.dll` + WebView2），日常使用与切换增强模式**零重复弹窗授权**。
  - 首次启动默认启用 **规则分流（Rule）**、**系统代理**、**TUN 增强模式（`mixed` 栈）**、**局域网与本地回环直连**、**国内流量直连** 以及 **中国大陆桌面应用自动直连**（内置微信、QQ、钉钉、飞书、百度网盘、网易云音乐、WPS 等跨平台进程规则，无需手工导入额外清单）。
- **双端一致的控制中心（7 大功能页 + 原生托盘）**：
  - 内置 **MetaCubeXD**（`v1.266.1`）实时控制台、**订阅与配置**（支持远程订阅、`clash://` / `clashfx://` 一键导入及单条分享链接转换）、**分流与规则**、**AI 锁定**、**多节点服务**、**节点诊断** 与 **偏好设置**。
  - 支持 Obsidian Dark 深色 / Alabaster Light 浅色双主题、简体中文 / English 实时切换，以及可定制的全局快捷键。
- **AI 专用锁定（Fail-Closed）**：
  - 将 **Claude**、**Codex**、**Antigravity（`agy`）** 桌面端、CLI 与 API 流量固定绑定到指定具体节点；节点不可用或协议受限时直接 `REJECT` 阻断，绝不悄然回落直连或其他出口。详见 [AI 专用锁定说明](docs/AI-PROXY-LOCK.md)。
- **多节点固定端口与自动巡检**：
  - 从 `127.0.0.1:21000` 起为每个节点分配持久化 HTTP + SOCKS5 混合监听端口，支持单节点 20 MB 下载测速、60 秒连通健康巡检，以及连续故障后经独立 `DIRECT` 通道自动刷新订阅。详见 [多节点服务说明](docs/NODE-SERVICES.md)。
- **纯净上游内核与事务级防崩回滚**：
  - 内置与在线升级均使用 100% 未修改的上游 `github.com/metacubex/mihomo`（`-tags with_gvisor`）。
  - 所有订阅重载、Profile Mixin 与规则合成先经过预校验再原子切换，并维护 `.runtime-last-good.yaml` 自动回滚快照；支持按 Wi-Fi / 网络环境自动挂起代理。
- **自动化云端构建与双通道在线升级**：
  - 支持 **Stable（正式版）** 与 **Lab（预发布）** 双通道，客户端启动后自动从 [GitHub Releases](https://github.com/xincun/ClashFX/releases) 检测对应系统与 CPU 架构的安装包并提示更新。

---

## 📥 下载与安装

从 [Releases](https://github.com/xincun/ClashFX/releases) 页面下载对应平台安装包：

| 平台与架构 | 推荐安装包 | 免安装 / 说明 |
| :--- | :--- | :--- |
| **macOS 12.0+**（Apple Silicon & Intel 通用） | `ClashFX.dmg` / `ClashFX.pkg` | Universal 通用二进制，安装时自动注册特权服务 |
| **Windows 10/11 x64 (`amd64`)** | `ClashFX-Setup-amd64.exe` | 推荐安装版（自动注册 `ClashFXService` 与 URL Scheme）；另有便携包 `ClashFX-Portable-amd64.zip` |
| **Windows 10/11 ARM64 (`arm64`)** | `ClashFX-Setup-arm64.exe` | 原生 ARM64 安装版（零模拟转译）；另有便携包 `ClashFX-Portable-arm64.zip` |

> 安装并启动后，在「订阅与配置」粘贴订阅链接，或通过浏览器点击 `clash://` / `clashfx://` 导入链接即可开始使用。

---

## 🏗️ 项目架构与目录结构

```text
ClashFX/
├── core-engine/            # 统一 Go 核心控制引擎（daemon + service + 全部业务逻辑与测试）
│   ├── cmd/clashfx-daemon/ # 用户态控制守护进程（Unix Socket / Named Pipe IPC）
│   └── cmd/clashfx-service/# 特权内核监管服务（LaunchDaemon / Windows Service）
├── macos/                  # macOS 宿主：纯 Swift AppKit + WKWebView、打包与签名脚本
│   ├── ui-macos/           # 托盘菜单、全局快捷键、WKWebView 控制中心宿主
│   ├── resources/          # 图标、MetaCubeXD 面板、GEO 数据库（随包离线部署）
│   └── scripts/            # build-core.py / build-daemon.py / build-ui.py / stage-app.py / package-{pkg,dmg}.py
├── windows/                # Windows 宿主：Qt 6.8 + Edge WebView2 宿主、Web SPA、Inno Setup
│   ├── ui-qt/              # C++17 Qt6 原生桌面壳 + web/ 控制中心 SPA 源码（双端共享）
│   ├── installer/          # Inno Setup 双架构安装脚本（clashfx.iss）
│   └── scripts/            # build-core.py / build-daemon.py / build-ui.ps1 / package-release.ps1
├── scripts/                # 本地一键打包（build-local.sh）与 Universal/兼容性门禁脚本
├── .github/workflows/      # GitHub Actions 自动化 CI（ci.yml）与三端发布流水线（release.yml）
└── docs/                   # 架构与专项功能文档（见 docs/README.md）
```

更多运行机制、IPC 安全边界与生命周期说明请参阅 [架构与文档索引](docs/README.md)。

---

## ⚙️ 配置目录与 URL Scheme

- **默认配置目录**：
  - macOS：`$HOME/.config/clashfx`
  - Windows：`%APPDATA%\ClashFX`
- **URL Scheme 快捷操作**：
  - 导入远程订阅：`clashfx://install-config?url=<URL编码订阅地址>&name=<名称>` 或 `clash://install-config?url=<URL编码订阅地址>&name=<名称>`
  - 重新加载配置：`clash://update-config`

---

## 🔨 从源码构建与自动化发版

### 运行 Go 核心引擎测试

```bash
cd core-engine
go test -race -count=1 ./...
```

### 本地构建 macOS Universal 包（`arm64` + `x86_64`）

前置要求：macOS 12.0+ 命令行工具 / Xcode（`swiftc`、`lipo`、`codesign`、`pkgbuild`、`hdiutil`）、Go 1.26+、Python 3。无需 `.xcodeproj` 或 CocoaPods：

```bash
./scripts/build-local.sh
# 产物输出到 macos/dist/ClashFX.app、macos/dist/ClashFX.pkg、macos/dist/ClashFX.dmg
```

### 本地构建 Windows 双架构包（`arm64` & `amd64`）

详见 [Windows 双架构构建指南](windows/README.md)。核心流水线：

1. `python3 windows/scripts/build-core.py`（编译上游 `mihomo_core.exe` 并校验官方 `wintun.dll` SHA256）
2. `python3 windows/scripts/build-daemon.py`（交叉/本机编译双架构 `clashfx-daemon.exe` 与 `clashfx-service.exe`）
3. `python3 windows/scripts/fetch-webview2-sdk.py`（拉取官方 WebView2 C++ SDK）
4. 在 Windows（Visual Studio 2022 + Qt 6.8 + Inno Setup 6）下运行 `windows\scripts\build-ui.ps1` 与 `windows\scripts\package-release.ps1`

### GitHub Actions 自动打包与发版

工作流定义在 [release.yml](.github/workflows/release.yml) 与 [ci.yml](.github/workflows/ci.yml)：

- 推送代码变更至 `release` 分支、推送版本 Tag（如 `v1.20.0`），或在 Actions 页面手动点击 `Run workflow` 即可并行编译 macOS Universal 与 Windows `amd64` / `arm64`，测试通过后自动汇总校验并发布至 [GitHub Releases](https://github.com/xincun/ClashFX/releases)。
- 仅修改 Markdown 文档的推送会自动跳过耗时的二进制打包（或可按需使用 `[skip ci]`）。

---

## 📄 许可证与致谢

本项目基于 [AGPL-3.0](LICENSE) 协议开源。

致谢：
- [MetaCubeX/mihomo](https://github.com/MetaCubeX/mihomo) — 代理核心引擎
- [MetaCubeX/metacubexd](https://github.com/MetaCubeX/metacubexd) — 内置 Web 控制台
- [MetaCubeX/meta-rules-dat](https://github.com/MetaCubeX/meta-rules-dat) — GeoIP / GeoSite 规则数据库
- [WireGuard Wintun](https://www.wintun.net/) — Windows 高性能 Layer 3 虚拟网卡驱动
