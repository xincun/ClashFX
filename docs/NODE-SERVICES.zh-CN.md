# 节点固定端口与自动直连刷新

入口：**菜单栏图标 → 多节点代理…**。这是本地个人构建增加的功能。

## 固定端口

- 默认启用，从 `21000` 开始给当前配置 `proxies` 中的每个节点分配独立端口。
- 只监听 `127.0.0.1`，同一个端口同时支持 HTTP 和 SOCKS5（含 UDP）。
- 每行“启用”复选框控制一个节点，可同时勾选多个；“全部启用/全部停止”批量修改，点击一次“保存并重载”统一应用。
- 未开启 AI 锁定时，节点端口始终使用对应节点，不随主代理组、规则模式或全局选择改变。主端口仍使用原来的分流规则。
- 可在表格中修改端口，再点击“保存并重载”。重名、重复端口、保留端口或监听冲突会报错，禁止静默改到其他节点。
- 订阅重新排序、密码轮换、节点暂时移除后重新出现，端口保持不变。若节点改名或服务端端口变化，但服务器和协议在新旧订阅中可以唯一对应，也会保留端口。
- 手动停止或暂时移除的节点保留其端口记录，重新启用使用原端口，避免旧应用地址后来被分配给另一个节点。不同配置使用独立的节点标识，并避免分配相同端口。
- 当前实现处理配置及 Profile Mixin 展开后的内联 `proxies`；`proxy-providers` 动态节点仍使用上游客户端原有的管理方式。

例如：`http://127.0.0.1:21000` 或 `socks5h://127.0.0.1:21000`。表格直接展示每个节点的固定端口、连通状态与下载速度。

Chrome 使用方式：在代理扩展中分别建立“节点 A”“节点 B”代理配置，协议选择 HTTP，服务器填 `127.0.0.1`，端口分别填写表格中的实际端口，再把不同网页的 URL 规则指向对应配置。HTTP 代理也支持 HTTPS 页面；选择 SOCKS5 时使用同一端口。停止节点不会删除其 Chrome 配置或回收端口，但停止期间该配置无法连接。

## 下载测速

“直连刷新订阅”后面的“测速”按钮只测试当前配置中已保存启用的节点，依次通过它们各自的固定端口下载数据。修改节点勾选或端口后先保存并重载。

- 测试地址采用 [Cloudflare 官方测速端点](https://github.com/cloudflare/speedtest/blob/main/README.md) `https://speed.cloudflare.com/__down?bytes=20000000`。
- 每个节点最多下载 20 MB 或运行 8 秒，计数收到的数据后直接丢弃，不保存下载文件。结果按接收正文期间的平均吞吐量显示为十进制 MB/s，与原有的连通延迟分别显示。
- 测速过程消耗代理流量；节点逐个测试，避免同时下载争抢带宽。测速期间按钮显示“停止测速”，可以随时取消。
- 表格显示等待、测速中、最终速度、失败或停止。停止和配置切换会取消当前传输并隔离迟到的回调；已停止的节点不参与测速。
- 测速期间暂停后台连通检测和订阅刷新。请求明确指定对应的本机代理端口，连接失败时不改用系统代理；错误响应、HTML 错误页和重定向不能形成有效测速结果。

下载测速不启动 Claude、Codex 或 Antigravity，不修改系统代理或主代理组选择。

## 自动检测与刷新

默认启用：每 **60 秒**检测一次，最多同时检测 3 个节点；单次探测超时 5 秒。先检测 Google 的 204 测试页，失败后用 Cloudflare 的 204 测试页复核。

任一节点连续 **3 次**确认不可用，会刷新当前配置对应的托管订阅。刷新尝试之间至少间隔 **300 秒**，下载失败同样进入冷却。检测间隔、连续失败次数和冷却时间都可以修改。

- 网络离线时暂停检测并清除连续失败计数。
- 内核控制接口不可用、检测取消或配置已经切换，不作为节点故障累计。
- 订阅使用一个绑定 **DIRECT** 出口的内部本机监听器下载，不通过远端代理节点。该方式也处理跨域 HTTP 跳转，不依赖当前主代理组能否连接。
- DIRECT 监听器不可用时，刷新报错；不会改用系统代理或其他节点重试。
- 下载失败或订阅内容校验失败保留原配置。运行配置重载由原有事务与回滚流程处理。
- “立即检测”可以手动检查；“直连刷新订阅”可主动刷新，不等待失败次数或冷却，但仍禁止并行刷新。

端口映射、设置和最后刷新尝试时间保存在应用偏好 `nodeServices.v1` 中；订阅凭据不写入源码。

**AI 专用锁定**可以与多节点端口和后台刷新同时使用。AI 流量优先走锁定节点，其他网页按 Chrome 指定的端口走；不通时不会回落到其他节点或 DIRECT。见 [AI 锁定说明](AI-PROXY-LOCK.md)。

## 本机编译

```bash
cd /Users/devtest/Desktop/workspace/github/ClashFX
bash scripts/build-local.sh
```

脚本使用 Go 1.22.12 编译原生内核，安装锁定的 CocoaPods 依赖，生成当前 Mac 架构的本地签名应用。默认以 macOS 12 为最低版本以适配本机 Xcode 27；需要时通过 `CLASHFX_LOCAL_MIN_MACOS` 覆盖。构建时跳过自动修改整个仓库的 SwiftFormat 步骤。

产物：`build_output/DerivedData/Build/Products/Release/ClashFX.app`。脚本从内到外重新做本地签名并验证。脚本不安装或启动应用，不更改当前系统代理。

本地重编译会改变应用的签名指纹。更新已安装版本后，ClashFX 可能提示安装/更新 Helper，要求在 macOS 中完成管理员授权以更新信任记录；授权本身不会开启系统代理或增强模式。

按用户要求，本地构建已屏蔽上游更新：不初始化 Sparkle，不自动检查、下载或安装官方版本；更新入口和频道切换显示为停用。更新此定制版本请保留源码分支并重新构建。

## 验证

```bash
python3 scripts/verify-node-services.py
python3 scripts/verify-node-watchdog.py
python3 scripts/verify-ai-multi-node.py
python3 scripts/verify-node-download-speed.py

xcodebuild -workspace ClashFX.xcworkspace -scheme ClashFX \
  -destination 'platform=macOS,arch=arm64' \
  -only-testing:ClashFXTests/NodeServicePolicyTests test \
  CLASHFX_SKIP_FORMAT=1 MACOSX_DEPLOYMENT_TARGET=12.0 CODE_SIGNING_ALLOWED=NO
```

集成脚本只启动独立临时内核和本机模拟节点，验证 HTTP/SOCKS5 固定出口、订阅重排后重载、手动换端口、在主出口被拒绝时 DIRECT 下载与跨域跳转，以及 DIRECT 不可用时不回落。它不会读取真实订阅、修改系统网络或启动已安装客户端。

后台巡检验收脚本直接运行生产巡检逻辑，使用隔离偏好和受控探测/下载回调验证触发、冷却、取消恢复、离线及配置切换。它不下载真实订阅。

下载测速脚本直接运行生产 URLSession 传输，使用本机流式代理模拟快慢节点、限量/限时结束、停止、错误页、重定向和连接中断，不访问外网或真实订阅。

这些检查不包含实机 TUN/特权 Helper 安装，也不代表已经在运行中的客户端执行了自动刷新。

实现依据：[mihomo 自定义监听器](https://wiki.metacubex.one/config/inbound/listeners/)、[mixed 监听器](https://wiki.metacubex.one/config/inbound/listeners/mixed/)。
