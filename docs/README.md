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
|     - 事务式重载、.runtime-last-good.yaml 回滚与内核零中断接管     |
|     - 12s 深度健康巡检、AI 锁定、多节点服务、系统代理与告警推送    |
+-------------------------------+-------------------------------+
                                | Privileged IPC (仅限可信本地二进制调用)
                                |   macOS:   LaunchDaemon Unix Socket
                                |   Windows: \\.\pipe\ClashFXService (SDDL + VerifyPipePeer)
                                v
+---------------------------------------------------------------+
|  3. 特权内核监管服务 (clashfx-service)                          |
|     - 以 root / SYSTEM 权限管理 TUN 虚拟网卡生命周期与睡眠唤醒自愈 |
|     - 崩溃自动退避守护重启 + 连续故障熔断 + 全量轮转日志落盘       |
|     - OS 级 AI 锁定防漏网护盾（Windows 动态 WFP / macOS pf）     |
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
- **内核崩溃自愈、深度巡检与强告警**：
  - `clashfx-service` 内置带代际标记的崩溃守护器：当 `mihomo_core` 意外退出时，自动以指数退避（`1s → 2s → 5s → 10s → 30s`）重新拉起；连续快速崩溃达 5 次触发 `crash_loop` 熔断（转入 60s 慢重试），并在 Windows 现代待机唤醒（`PBT_APMRESUMEAUTOMATIC` / `PBT_APMRESUMESUSPEND`）时主动自检恢复；正常停机时在 Windows 优先发送 `CTRL_BREAK_EVENT` 以便优雅回收 Wintun。
  - `clashfx-daemon` 重启时自动零中断接管仍在特权服务中运行的内核，并每 12 秒执行深度健康检查（验证 Mihomo `/version` 控制器与 Wintun/`utun` 网卡活跃度，连续 3 次异常自动强制重启内核）。
  - 发生内核掉线、重启熔断或 AI 护盾拦截时，原生托盘即时切换红色告警图标并弹出 OS 通知，控制中心弹出告警横幅支持一键重启内核。
- **OS 级 AI 锁定防漏网护盾**：当 AI 锁定开启时，`clashfx-service` 在内核崩溃、升级切换或开机空窗期通过 Windows 应用粒度动态 WFP 会话或 macOS `pf` 锚点阻断受保护 AI 客户端直连公网，详见 [AI-PROXY-LOCK.md](AI-PROXY-LOCK.md)。

---

## 持久化全量日志 / Persistent Rotating Logs

所有核心进程均通过 `core-engine/internal/applog` 实时落盘并按 **单文件 10 MiB × 保留 5 份历史归档** 自动轮转，同时在控制中心「节点诊断」页及诊断报告中直接暴露路径与脱敏尾部摘要，彻底避免故障现场无日志可查：

| 日志文件 | 默认位置 | 内容说明 |
| :--- | :--- | :--- |
| `mihomo-core.log` | `<ConfigDir>/logs/` | `mihomo_core` 完整标准输出/错误日志（实时刷盘，无内存截断） |
| `clashfx-daemon.log` | `<ConfigDir>/logs/` | 用户守护进程生命周期、健康巡检、系统代理变更、订阅重载与自愈记录 |
| `clashfx-service-events.log` | `<ConfigDir>/logs/` | 特权服务针对当前用户配置目录的内核启停、崩溃退出码、退避重启与 AI 护盾事件镜像 |
| `clashfx-service.log` | Windows: `%ProgramData%\ClashFX\logs\`<br>macOS: `/Library/Application Support/ClashFX/logs/`（及 `/var/log/clashfx-service.log`） | 系统级特权服务完整运行日志与开机护盾恢复记录 |

> `<ConfigDir>` 默认为 macOS `$HOME/.config/clashfx`、Windows `%APPDATA%\ClashFX`。
