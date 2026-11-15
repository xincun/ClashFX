# AI Proxy Lock / AI 专用锁定

AI 专用锁定在 **macOS** 与 **Windows** 上共用同一套策略引擎（实现位于 `core-engine/internal/ailock/`），将 **Claude**、**Codex** 和 **Antigravity (`agy`)** 的桌面端、CLI 以及精确匹配的 API / 登录端点固定到一个具体代理节点，并在节点故障或协议不受支持时**硬阻断（Fail-Closed）**，避免泄漏到 `DIRECT` 或自动切换代理组。

---

## 入口与工作方式

- **入口**：控制中心左侧 **AI 锁定** 页面，或系统托盘菜单 → **AI 锁定**。
- **目标限制**：只能绑定具体出站节点；`DIRECT`、`REJECT`、`COMPATIBLE`、`PASS`、`REJECT-DROP` 等内置策略项不会作为可锁定目标。
- **联动保护**：启用 AI 锁定后会自动锁定为 **Rule（规则）模式 + 增强模式（TUN）+ 系统代理**，并保存锁定前的网络状态 snapshot（停用锁定时恢复）。在 AI 锁定生效期间，普通退出 ClashFX 会遵循 Fail-Closed 策略阻止未授权回落直连。

---

## 跨平台匹配规则 (`clashfx-ai-lock`)

规则定义集中在 `core-engine/internal/profile/ruleset.go` 与 `core-engine/internal/ailock/policy.go`，运行时以最高优先级规则置于普通规则与国内直连规则之前，并强制开启 `find-process-mode` 进程识别：

1. **进程名正则 (`PROCESS-NAME-REGEX`)**：匹配 `claude`、`codex`、`codex-app-server`、`chatgpt`、`antigravity`、`antigravity-cli`、`agy` 及其 Helper / `.exe` 变体。
2. **安装路径正则 (`PROCESS-PATH-REGEX`)**：
   - **macOS**：匹配 `Claude.app`、`Codex.app`、`CodexCLI.app`、`ChatGPT.app`、`Antigravity.app` 应用 Bundle 内子进程（例如隐藏在应用内部的语言服务器）。
   - **Windows**：匹配 `Programs` / `Program Files` / `AppData\Local\AnthropicClaude` / `WindowsApps` / `.local\bin` / `.antigravity` / `.gemini\antigravity-cli` 目录下的对应二进制。
   - 不会宽泛匹配整个浏览器、编辑器主进程或其他无关语言服务器，避免误伤。
3. **精确域名后缀 (`DOMAIN-SUFFIX`)**：覆盖 `claude.ai`、`claude.com`、`anthropic.com`、`claudeusercontent.com`、`openai.com`、`chatgpt.com`、`oaistatic.com`、`oaiusercontent.com`、`oaistatsig.com`、`openaimerge.com`、`antigravity.google` 及受控的 Google/API 服务端点。

### Fail-Closed 规则生成

运行时会在通用规则前插入两条绑定 inline provider `clashfx-ai-lock` 的规则：

```yaml
- RULE-SET,clashfx-ai-lock,<已选具体节点>
- RULE-SET,clashfx-ai-lock,REJECT
```

即使所选节点不支持特定协议（如 UDP）导致 mihomo 跳过第一条匹配，第二条 `REJECT` 规则也会立刻截断连接，确保不会穿透到后续通用分流规则或 `DIRECT`。

---

## 与多节点固定端口协同工作

当 **多节点服务（`21000+`）** 与 **AI 专用锁定** 同时开启时，每个多节点固定端口监听器会自动绑定独立的子规则列表：

1. 匹配 `clashfx-ai-lock` 的流量 → 强制走**锁定节点**；失败则走 `REJECT` 阻断；
2. 其他流量 → 走该固定端口绑定的**目标节点**；协议不兼容时阻断，不回落 `DIRECT`。

后台 `DIRECT` 订阅刷新监听端口同样内置内部直连白名单并隔离通用规则，可与 AI 锁定安全共存。

---

## 自动化测试验证

```bash
cd core-engine
go test -race -count=1 ./internal/ailock/...
```
