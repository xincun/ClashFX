# AI Proxy Lock / AI 专用锁定

AI 专用锁定在 **macOS** 与 **Windows** 上共用同一套策略引擎（实现位于 `core-engine/internal/ailock/`），将 **Claude**、**Codex** 和 **Antigravity (`agy`)** 的桌面端、CLI 以及精确匹配的 API / 登录端点固定到一个具体代理节点，并在节点故障或协议不受支持时**硬阻断（Fail-Closed）**，避免泄漏到 `DIRECT` 或自动切换代理组。

---

## 入口与工作方式

- **入口**：控制中心左侧 **AI 锁定** 页面，或系统托盘菜单 → **AI 锁定**。
- **目标限制**：只能绑定具体出站节点；`DIRECT`、`REJECT`、`COMPATIBLE`、`PASS`、`REJECT-DROP` 等内置策略项不会作为可锁定目标。
- **联动保护**：启用 AI 锁定后会自动锁定为 **Rule（规则）模式 + 增强模式（TUN）+ 系统代理**，并保存锁定前的网络状态 snapshot（停用锁定时恢复）。正常退出 ClashFX 时会恢复系统代理以确保正常网页及 Captive Portal 认证可用；若此时 AI 锁定处于开启状态，交互式退出会弹窗提醒是否先关闭运行中的受保护 AI 工具，再依次恢复系统代理并停用 TUN，避免 AI 客户端在退出后直连窜网。

---

## 跨平台匹配规则 (`clashfx-ai-lock`)

规则定义集中在 `core-engine/internal/profile/ruleset.go` 与 `core-engine/internal/ailock/policy.go`，运行时以最高优先级规则置于普通规则与国内直连规则之前，并强制开启 `find-process-mode` 进程识别：

1. **进程名正则 (`PROCESS-NAME-REGEX`)**：匹配 `claude`、`codex`、`codex-app-server`、`chatgpt`、`antigravity`、`antigravity-cli`、`agy`、`language_server_(windows|macos)_*` 及其 Helper / `.exe` 变体。
2. **安装路径正则 (`PROCESS-PATH-REGEX`)**：
   - **macOS**：匹配 `Claude.app`、`Codex.app`、`CodexCLI.app`、`ChatGPT.app`、`Antigravity.app` 及 `extensions/antigravity/` 应用 Bundle 内子进程（包含内置语言服务器）。
   - **Windows**：匹配 `Programs` / `Program Files` / `AppData\Local\AnthropicClaude` / `WindowsApps` / `.local\bin` / `.antigravity` / `.gemini\antigravity-cli` / `extensions\antigravity\` 目录下的对应二进制。
   - 不会宽泛匹配整个浏览器、编辑器主进程或其他无关语言服务器，避免误伤。
3. **精确域名后缀 (`DOMAIN-SUFFIX`)**：覆盖 `claude.ai`、`claude.com`、`anthropic.com`、`claudeusercontent.com`、`openai.com`、`chatgpt.com`、`oaistatic.com`、`oaiusercontent.com`、`oaistatsig.com`、`openaimerge.com`、`antigravity.google` 及受控的 Google/API 服务端点。

### Fail-Closed 规则生成（内核层）

运行时会在通用规则前插入两条绑定 inline provider `clashfx-ai-lock` 的规则：

```yaml
- RULE-SET,clashfx-ai-lock,<已选具体节点>
- RULE-SET,clashfx-ai-lock,REJECT
```

即使所选节点不支持特定协议（如 UDP）导致 mihomo 跳过第一条匹配，第二条 `REJECT` 规则也会立刻截断连接，确保不会穿透到后续通用分流规则或 `DIRECT`。

---

## 系统级 Fail-Closed 防漏网护盾（内核崩溃 / TUN 掉线保护）

仅靠 `mihomo_core` 内部规则无法覆盖“内核崩溃、Wintun 网卡意外移除、在线升级切换或开机尚未拉起内核”的空窗期（例如 Go 编写的语言服务器 `language_server_windows_*` 不读取 Windows 注册表系统代理，一旦 TUN 消失就会回落物理网卡裸连公网）。

为此，`clashfx-service` 特权服务独立于 `mihomo_core` 维护一层 **OS 级防漏网护盾**（实现位于 `core-engine/internal/ailock/discover.go` 与 `core-engine/internal/service/aiguard*.go`）：

- **Windows（应用粒度动态 WFP 会话）**：
  - 通过原生 Win64 `fwpuclnt.dll` 创建 **动态会话（`FWPM_SESSION_FLAG_DYNAMIC`）**——服务退出、卸载或系统重启后内核过滤规则自动清空，绝不永久锁死网络。
  - 仅作用于自动发现并持续刷新（每 30 秒）的受保护 AI 客户端可执行文件（如 `Antigravity.exe`、`language_server_windows_*.exe`、`claude.exe`、`codex.exe`、`ChatGPT.exe` 等，自动排除 ClashFX 自身进程），**不影响浏览器、办公软件与 ClashFX 订阅刷新**。
  - 放行 `127.0.0.1` / `::1` 本地回环、局域网私有网段与用户配置的 `TunRouteExcludes`；在 `mihomo_core` 与 `ClashFX` Wintun 网卡存活时放行当前 Wintun 接口 LUID（`armed_normal`）。
  - 一旦内核崩溃、Wintun 掉线、进入重启退避或系统开机重启尚未恢复（由 `%ProgramData%\ClashFX\ai-guard-state.json` 恢复策略），护盾立刻收紧为 **`blocking_leaks`**，在 OS 网络栈层直接拒绝受保护 AI 进程的公网直连，直到 TUN 恢复健康或用户显式关闭 AI 锁定 / 卸载服务。
- **macOS（`pf` 故障窗口 Anchor）**：
  - 使用引用计数的 `pfctl -E` / `-X` 挂载内置锚点 `com.apple/clashfx.ailock`（不修改 `/etc/pf.conf`）。
  - `mihomo_core` 与 `utun` 正常运行时锚点保持为空（`armed_normal`）；一旦发生内核崩溃、重启或在线升级切换空窗，立即在锚点内阻断当前桌面用户的公网直连出站（`blocking_leaks`，保留回环、局域网与排除网段），内核与 `utun` 恢复后自动清空放行。
- **系统代理与 CLI 终端兜底**：
  - 当 AI 锁定开启且内核异常停止时，`clashfx-daemon` 会刻意保留系统代理指向本地端口（Fail-Closed），而未开启 AI 锁定时则自动释放失效系统代理以免普通网页白屏。
  - 对于共享通用解释器路径的命令行工具（如 `node` / `python` / `curl`），控制中心 **AI 锁定** 页面提供一键复制 **PowerShell / Bash** 显式代理环境变量命令（`HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY`），便于终端层同样实现 Fail-Closed。
- **异常强提醒**：
  - 内核掉线、触发护盾拦截或进入重启熔断时，系统托盘切换为红色告警图标并弹出系统级高优先级通知，控制中心顶部显示醒目告警条，支持托盘与页面**一键重启代理内核**或查看持久化日志。

---

## 与多节点固定端口协同工作

当 **多节点服务（`21000+`）** 与 **AI 专用锁定** 同时开启时，每个多节点固定端口监听器会自动绑定独立的子规则列表：

1. 匹配 `clashfx-ai-lock` 的流量 → 强制走**锁定节点**；失败则走 `REJECT` 阻断；
2. 其他流量 → 走该固定端口绑定的**目标节点**；协议不兼容时阻断，不回落 `DIRECT`。

后台 `DIRECT` 订阅刷新监听端口同样内置内部直连白名单并隔离通用规则，可与 AI 锁定安全共存。

---

## 自动化测试验证

```bash
# 策略匹配、进程发现、内核守护与护盾状态机单元测试（自动隔离宿主系统代理与 LaunchAgent）
make test-go-race PKG="./internal/ailock/... ./internal/service/... ./internal/daemon/..."
```

在 Windows 管理员权限下可额外运行真实 Win64 WFP 内核与 `mihomo_core` 强杀自愈端到端测试：

```powershell
$env:CLASHFX_LIVE_WINDOWS_WFP_VERIFY = "1"
go test -v -run "TestWFPStructABI|TestWindowsLiveWFPAndCoreSupervisor" ./internal/service
```
