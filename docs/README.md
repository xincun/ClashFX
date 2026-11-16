# ClashFX 架构与文档索引 / Architecture & Docs

## 文档目录 / Index

- [../README.md](../README.md) — 中文主说明与快速上手 / Simplified Chinese README
- [../README_en.md](../README_en.md) — English README
- [AI-PROXY-LOCK.md](AI-PROXY-LOCK.md) — AI 专用锁定（Claude / Codex / Antigravity 进程与域名 Fail-Closed 保护）
- [NODE-SERVICES.md](NODE-SERVICES.md) — 多节点固定端口（`21000+`）、20 MB 测速与 60s 巡检自动刷新
- [../windows/README.md](../windows/README.md) — Windows (`arm64` & `amd64`) 编译与 Inno Setup 打包指南

---

## 运行时四层架构 / 4-Layer Runtime Model

ClashFX 在 macOS 与 Windows 上采用同一套四层进程分工模型，核心业务逻辑全部沉淀在 `core-engine/`（Go）：

```text
+---------------------------------------------------------------+
|  1. 原生桌面宿主 + 共享 Web SPA                                 |
|     - macOS: ClashFX (Swift AppKit + WKWebView)               |
|     - Windows: clashfx-ui.exe (Qt 6.8 + Edge WebView2)        |
|     - 前端控制中心: windows/ui-qt/web/ + 内置 MetaCubeXD        |
+-------------------------------+-------------------------------+
                                | Local IPC (JSON-over-line)
                                |   macOS:   /tmp/clashfx-<uid>/daemon.sock (0600)
                                |   Windows: \\.\pipe\ClashFXDaemon-<user> (DACL)
                                v
+---------------------------------------------------------------+
|  2. 用户态控制守护进程 (clashfx-daemon)                         |
|     - 订阅下载/解析/分享链接转换、Profile Mixin 与内置规则合成     |
|     - 事务式重载与 .runtime-last-good.yaml 故障自动回滚           |
|     - AI 专用锁定 / 多节点服务 / 系统代理管理 / 网络自动挂起 / 更新检测 |
+-------------------------------+-------------------------------+
                                | Privileged IPC (仅限可信本地二进制调用)
                                |   macOS:   LaunchDaemon Unix Socket
                                |   Windows: \\.\pipe\ClashFXService (SDDL + VerifyPipePeer)
                                v
+---------------------------------------------------------------+
|  3. 特权内核监管服务 (clashfx-service)                          |
|     - 以 root / SYSTEM 权限管理 TUN 虚拟网卡生命周期              |
|     - 通过 SAFE_PATHS 与 Win32 Job Object / 子进程守护监管内核    |
+-------------------------------+-------------------------------+
                                |
                                v
+---------------------------------------------------------------+
|  4. 代理内核 (mihomo_core)                                      |
|     - 100% 未修改上游 github.com/metacubex/mihomo (with_gvisor) |
+---------------------------------------------------------------+
```

---

## 启动与退出生命周期 / Startup & Exit Lifecycle Parity

macOS 与 Windows 严格对齐以下生命周期契约：

1. **安装即就绪**：
   - macOS (`ClashFX.pkg` postinstall) 与 Windows (`ClashFX-Setup-{arch}.exe`) 在安装阶段自动安装并启动 `clashfx-service`（分别通过 `LaunchDaemon` 与 `ClashFXService` Windows 服务），同时离线释放 GeoIP/GeoSite 归档与控制中心资源。
   - 首次启动默认生成/应用 `Rule` 出站模式、系统代理、TUN 增强模式（`mixed` 栈）、局域网直连、国内规则直连以及国内桌面应用直连。
2. **关闭窗口 vs 完全退出**：
   - 点击控制中心关闭按钮仅隐藏/关闭前端窗口，驻留托盘与代理服务继续运行（单实例唤醒再次显示窗口）。
   - 从托盘菜单执行退出（或 `quit_daemon`）时：当 **AI 专用锁定**开启时，交互式退出会提示是否先关闭受保护的 AI 工具以防直连窜网；随后按序恢复系统代理（保障退出后正常浏览与 Wi-Fi Portal 认证）、停止多节点固定端口监听器与巡检协程，并通知特权服务停止 `mihomo_core` 回收 TUN 网卡，最后有序退出守护进程与 UI，不留孤儿 `mihomo_core` 进程。

---

## 安全与可靠性边界 / Security & Reliability Boundaries

- **零非标核心参数**：`clashfx-service` 仅使用上游 mihomo 标准参数 `-d`、`-f`、`-ext-ui` 启动同目录受信任的 `mihomo_core`；进程生命周期由平台原生监管（macOS 进程监管 / Windows `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`）与 `SAFE_PATHS` 沙箱边界保障，支持在线原地热更新内核。
- **IPC 权限隔离**：
  - 用户守护进程套接字/命名管道仅允许当前登录用户访问（Windows 通过 SDDL 限制为 LocalSystem + 当前用户 SID）。
  - 特权服务在执行 `start_core` / `stop_core` / `upgrade_core` 前会校验调用方进程（Windows `VerifyPipePeer` + 路径校验、`home_dir` 绝对非 UNC 路径与 `config_file`  containment 校验）。
- **事务级防崩回滚**：配置写入 `.runtime-config.yaml` 前会通过 `mihomo_core -t` 与控制端口就绪检查；校验失败或启动异常时自动恢复最近一次验证通过的 `.runtime-last-good.yaml`。
