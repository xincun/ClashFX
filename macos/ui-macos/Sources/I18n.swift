import Foundation

final class I18n {
    static let shared = I18n()

    private let lock = NSLock()
    private var currentLanguage = "zh_CN"
    private var observers: [(UUID, (String) -> Void)] = []

    private init() {}

    var language: String {
        lock.lock()
        defer { lock.unlock() }
        return currentLanguage
    }

    var isChinese: Bool {
        language != "en_US"
    }

    @discardableResult
    func addObserver(_ handler: @escaping (String) -> Void) -> UUID {
        let id = UUID()
        lock.lock()
        observers.append((id, handler))
        lock.unlock()
        return id
    }

    func setLanguage(_ lang: String) {
        let trimmed = lang.trimmingCharacters(in: .whitespacesAndNewlines)
        let normalized = (trimmed.caseInsensitiveCompare("en_US") == .orderedSame ||
                          trimmed.caseInsensitiveCompare("en") == .orderedSame) ? "en_US" : "zh_CN"
        var notifyList: [(String) -> Void] = []
        lock.lock()
        if currentLanguage != normalized {
            currentLanguage = normalized
            notifyList = observers.map { $0.1 }
        }
        lock.unlock()

        if !notifyList.isEmpty {
            let fire = {
                for cb in notifyList {
                    cb(normalized)
                }
            }
            if Thread.isMainThread {
                fire()
            } else {
                DispatchQueue.main.async(execute: fire)
            }
        }
    }

    static func pick(_ zh: String, _ en: String) -> String {
        shared.isChinese ? zh : en
    }

    static func translateBackendText(_ text: String) -> String {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty || !shared.isChinese {
            return trimmed
        }

        let exactMap: [String: String] = [
            "Waiting for the next node check": "等待下一轮节点 204 连通性巡检",
            "The active configuration has no inline proxy nodes": "当前配置不包含可分配独立端口的内联代理节点",
            "Checking node connectivity...": "正在巡检节点 204 连通性...",
            "Node connectivity check finished": "节点 204 连通性巡检已完成",
            "Node failure detected; subscription refresh is cooling down": "检测到节点故障；直连刷新订阅正处于冷却时间内",
            "Refreshing subscription through DIRECT...": "正在通过直连通道刷新订阅...",
            "Subscription refreshed directly and reloaded": "已通过直连通道刷新订阅并完成重载",
            "Enable and save at least one node before testing download speed": "请先至少勾选并保存一个启用端口的节点后再测速",
            "Testing enabled nodes' download speeds...": "正在测试已启用节点的 20MB 下载速度...",
            "Download speed test stopped": "下载测速已停止",
            "Download speed test finished": "下载测速已完成",
            "proxy core is not running": "代理内核当前未运行",
            "multi-node ports are disabled": "多节点固定端口总开关当前已关闭",
            "no enabled node ports": "没有已启用的节点固定端口",
            "direct-refresh listener port is not active": "直连刷新专用监听端口未就绪（请先保存启用多节点端口）",
            "system proxy cannot be disabled while AI Proxy Lock is active": "AI 专用锁定开启期间系统代理受保护不可关闭",
            "enhanced TUN mode cannot be disabled while AI Proxy Lock is active": "AI 专用锁定开启期间增强模式 (TUN) 受保护不可关闭",
            "outbound mode is locked to Rule while AI Proxy Lock is active": "AI 专用锁定开启期间出站模式固定为规则模式",
            "Not connected to clashfx-daemon": "尚未连接到 clashfx-daemon 后台服务",
            "Disconnected from clashfx-daemon": "与 clashfx-daemon 后台服务的连接已断开",
        ]

        if let hit = exactMap[trimmed] {
            return hit
        }

        let directFailPrefix = "Direct refresh failed; previous configuration retained ("
        if trimmed.hasPrefix(directFailPrefix) {
            let suffix = trimmed.dropFirst(directFailPrefix.count)
            return "直连刷新订阅失败，已保留原配置 (" + suffix
        }
        if trimmed.contains("privileged TUN service is not installed or running") {
            return "提权 TUN 服务尚未安装或未运行，请允许管理员授权完成初始化。"
        }
        if trimmed.localizedCaseInsensitiveContains("duplicate node listener port") {
            return "节点监听端口存在重复冲突（\(trimmed)）"
        }
        if trimmed.localizedCaseInsensitiveContains("collides with reserved port") {
            return "节点端口与混合端口或 API 端口冲突（\(trimmed)）"
        }
        return trimmed
    }
}

func L(_ zh: String, _ en: String) -> String {
    I18n.pick(zh, en)
}
