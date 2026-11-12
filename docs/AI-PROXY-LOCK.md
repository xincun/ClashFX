# AI Proxy Lock / AI 专用锁定

AI 专用锁定将受支持的 Claude、Codex、Antigravity 流量统一固定到一个具体节点。节点不可用时阻断连接，不回落到其他节点或直连。它沿用原有锁定事务、TUN 接管、系统代理和规则模式保护；偏好键仍使用 `claudeProxyLockEnabled` / `claudeProxyLockTarget`，保留升级前的设置。

## 使用

菜单栏图标 → **AI 专用锁定…** → 选择具体节点 → **启用锁定**。不能选择 DIRECT、REJECT、COMPATIBLE、PASS、REJECT-DROP 或自动切换的代理组；这些内置项不会显示在候选列表中。

启用后自动使用增强模式、规则模式和系统代理。停用锁定后才能关闭这些保护设置。锁定状态直接退出 ClashFX 会保留指向停止内核的系统代理，使遵循该系统代理的应用无法联网；需要恢复原网络代理时先停用锁定。

## 桌面端与 CLI

| 产品 | 进程与安装路径匹配 | 域名兜底 |
| --- | --- | --- |
| Claude | Claude、Claude Helper、`claude`，及版本目录形式的原生 CLI | claude.ai、claude.com、anthropic.com、claudeusercontent.com |
| Codex | Codex、`codex`、codex-app-server、ChatGPT 宿主与相应辅助进程/应用路径 | chatgpt.com、openai.com，以及 OpenAI 自有静态资源/内容域名 |
| Antigravity | Antigravity、辅助进程、`agy`、antigravity-cli；在应用目录和 Antigravity 专用目录中的后台程序 | antigravity.google，以及明确列出的 Google AI 服务和登录端点 |

进程规则不匹配整个 Chrome、VS Code、node 或 python，也不按通用 `language_server` 名称全局捕获。Antigravity 后台程序有多个版本命名，使用所属应用/专用目录路径兜底，避免把其他编辑器的语言服务器一起锁定。

Google API 使用精确域名规则，例如 `cloudcode-pa.googleapis.com`、`businessaicode.googleapis.com`、区域化企业服务、`generativelanguage.googleapis.com`，以及 `accounts.google.com` / `oauth2.googleapis.com` 登录端点；不把整个 google.com 或 googleapis.com 加入锁定。共享登录端点的规则也可能影响浏览器中的其他 Google 登录。

这些规则在运行时放到原规则前面，并启用进程查找。每条 AI 匹配后紧跟相同匹配条件的 REJECT，避免 mihomo 因锁定节点不支持 UDP 而跳过它后落到其他出口。

## 与多节点代理一起使用

多节点窗口的端口可以同时运行。AI 锁定开启时，每个受管理端口使用单独的子规则：

1. AI 进程或服务域名 → 锁定节点；失败则阻断。
2. 其他页面 → 该端口对应的节点。
3. 该节点不支持的协议 → 阻断，避免回落 DIRECT。

内部订阅刷新端口同样遵循 AI 保护规则，普通订阅下载仍走 DIRECT。后台巡检和自动刷新可以与 AI 锁定同时运行。

Chrome 扩展应指向本版本“多节点代理…”窗口给出的本机端口。指向 XrayGUI 或其他代理软件的端口不会经过本版本的子规则。用户自己编写的自定义 listeners、其他代理栈和 VPN 同样不属于这些受管理端口的保护范围。

CLI 的非标准包装器或自定义网关需要能被进程/路径规则识别，或显式补充网关域名规则。锁定的是具体节点；供应商更换该节点的外部 IP 时，实际 IP 仍会改变。

## 信息来源与验证范围

- [Claude Code 官方网络要求](https://code.claude.com/docs/en/network-config)
- [OpenAI 官方网络域名与 Codex WebSocket 要求](https://help.openai.com/en/articles/9247338-network-recommendations-for-chatgpt-errors-on-web-and-apps)
- [Codex 官方 CLI 二进制定义](https://github.com/openai/codex/blob/main/codex-rs/cli/Cargo.toml)
- [Antigravity 官方 CLI 安装与 agy 命令](https://antigravity.google/docs/getting-started?tab=cli)
- [Antigravity 官方企业网络端点](https://antigravity.google/docs/enterprise)

验证使用静态进程/路径样例、模拟流量和独立内核。按用户要求，不启动本机 Claude、Codex、Antigravity 或它们的 CLI；这些检查不代表已经使用真实账户做端到端登录验证。
