import Cocoa
import Foundation

final class TrayController: NSObject, NSMenuDelegate, URLSessionDataDelegate {
    private let client: DaemonClient
    private let consoleWindow: ConsoleWindowController
    private let smokeMode: Bool
    private let hotkeys = HotkeyManager()
    private let statusItem: NSStatusItem
    private let trayMenu = NSMenu()

    private var connected = false
    private var snapshot: [String: Any] = [:]
    private var isMenuOpen = false
    private var menuDirty = false
    private var lastIconKey = ""
    private var cachedPillIcon: NSImage?
    private var lastVisualKey = ""
    private var lastTooltip = ""
    private var lastMenuSig = ""
    private var i18nObserverID: UUID?
    private(set) var forceRestoreProxyOnQuit = false
    private(set) var headlessQuitOnTerminate = false

    private var liveUpRate: Int64 = 0
    private var liveDownRate: Int64 = 0
    private var trafficSession: URLSession?
    private var trafficTask: URLSessionDataTask?
    private var trafficBuffer = Data()
    private var activeTrafficPort = 0
    private var activeTrafficSecret = ""
    private var trafficReconnectWork: DispatchWorkItem?
    private weak var liveSpeedMenuItem: NSMenuItem?

    private var nextTag = 100
    private var actionMap: [Int: () -> Void] = [:]

    private static let speedUnits = ["B/s", "KB/s", "MB/s", "GB/s", "TB/s"]
    private static let speedScales: [Double] = [
        1.0,
        1024.0,
        1024.0 * 1024.0,
        1024.0 * 1024.0 * 1024.0,
        1024.0 * 1024.0 * 1024.0 * 1024.0,
    ]
    private static let speedFont = NSFont.monospacedDigitSystemFont(ofSize: 9.0, weight: .medium)
    private static let speedSampleText = "999.9KB/s"

    init(client: DaemonClient, consoleWindow: ConsoleWindowController, showConsoleInitially: Bool, smokeMode: Bool = false) {
        self.client = client
        self.consoleWindow = consoleWindow
        self.smokeMode = smokeMode
        self.statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        super.init()

        trayMenu.delegate = self
        statusItem.menu = trayMenu

        consoleWindow.onHotkeyRecordingChanged = { [weak self] active in
            self?.hotkeys.setSuspended(active)
        }

        i18nObserverID = I18n.shared.addObserver { [weak self] _ in
            self?.rebuildVisualsAndMenu(forceMenu: true)
        }

        client.onConnectionChanged = { [weak self] conn in
            self?.onConnectionChanged(conn)
        }
        client.onStateChanged = { [weak self] snap in
            self?.onStateChanged(snap)
        }

        rebuildVisualsAndMenu(forceMenu: true)

        if showConsoleInitially {
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.15) { [weak self] in
                self?.openConsole()
            }
        }
    }

    deinit {
        stopTrafficStream(resetRates: false)
        if let id = i18nObserverID {
            _ = id
        }
    }

    // MARK: - NSMenuDelegate

    func menuWillOpen(_ menu: NSMenu) {
        isMenuOpen = true
    }

    func menuDidClose(_ menu: NSMenu) {
        isMenuOpen = false
        if menuDirty {
            menuDirty = false
            rebuildMenuContents()
        }
    }

    // MARK: - Daemon & Navigation Entry Points

    private func resolveOpenMainWindowChord(_ settings: [String: Any]) -> String {
        let hk = (settings["hotkeys"] as? [String: Any]) ?? [:]
        return ((hk["open_main_window"] as? String) ?? "Fn+F12")
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }

    func onConnectionChanged(_ conn: Bool) {
        connected = conn
        if !conn && snapshot.isEmpty && !smokeMode {
            _ = hotkeys.applyHotkeys(
                openMainWindow: "Fn+F12",
                toggleSystemProxy: "",
                switchOutboundMode: "",
                onOpenMainWindow: { [weak self] in self?.onOpenMainWindowHotkey() },
                onToggleSystemProxy: { [weak self] in self?.onToggleSystemProxyHotkey() },
                onSwitchOutboundMode: { [weak self] in self?.onSwitchModeHotkey() }
            )
        }
        consoleWindow.applySnapshot(connected: conn, snapshot: snapshot)
        rebuildVisualsAndMenu()
    }

    func onStateChanged(_ snap: [String: Any]) {
        connected = true
        snapshot = snap

        let settings = (snap["settings"] as? [String: Any]) ?? [:]
        let lang = (settings["language"] as? String) ?? "zh_CN"
        I18n.shared.setLanguage(lang)

        let hk = (settings["hotkeys"] as? [String: Any]) ?? [:]
        let openChord = resolveOpenMainWindowChord(settings)
        let toggleChord = (hk["toggle_system_proxy"] as? String) ?? ""
        let modeChord = (hk["switch_outbound_mode"] as? String) ?? ""
        if !smokeMode {
            let failed = hotkeys.applyHotkeys(
                openMainWindow: openChord,
                toggleSystemProxy: toggleChord,
                switchOutboundMode: modeChord,
                onOpenMainWindow: { [weak self] in self?.onOpenMainWindowHotkey() },
                onToggleSystemProxy: { [weak self] in self?.onToggleSystemProxyHotkey() },
                onSwitchOutboundMode: { [weak self] in self?.onSwitchModeHotkey() }
            )
            if !failed.isEmpty {
                showAlert(
                    title: "ClashFX",
                    message: L("全局快捷键已被其他程序占用或格式无效: ", "Global hotkey unavailable or already in use: ")
                        + failed.joined(separator: ", "),
                    warning: true
                )
            }
        }

        consoleWindow.applySnapshot(connected: true, snapshot: snap)
        rebuildVisualsAndMenu()
    }

    func handleExternalCommand(_ message: String) {
        if message.lowercased().hasPrefix("uri:") {
            let uri = String(message.dropFirst(4)).trimmingCharacters(in: .whitespacesAndNewlines)
            importSchemeUri(uri)
            return
        }
        if message == "ACTIVATE" || message == "--open-console" {
            openConsole()
        } else if message == "--open-ai-lock" {
            openAiLockDialog()
        } else if message == "--open-multi-node" {
            openMultiNodeDialog()
        } else if message == "--open-rules" {
            openRuleEditorDialog()
        } else if message == "--open-active-rules" {
            openActiveRulesDialog()
        } else if message == "--open-subscriptions" {
            openSubscriptionsDialog()
        } else if message == "--open-diagnostics" {
            openDiagnosticsDialog()
        } else if message == "--open-settings" {
            openSettingsDialog()
        } else if message == "--quit" {
            quitAll(forceRestoreProxy: false, headless: true)
        } else if message == "--quit-uninstall" {
            quitAll(forceRestoreProxy: true, headless: true)
        } else if message.lowercased().hasPrefix("clash://") || message.lowercased().hasPrefix("clashfx://") {
            importSchemeUri(message)
        }
    }

    func openConsole() {
        consoleWindow.navigateTo(page: 0)
        consoleWindow.presentWindow()
    }

    func openSubscriptionsDialog() {
        consoleWindow.navigateTo(page: 1)
        consoleWindow.presentWindow()
    }

    func openRuleEditorDialog() {
        consoleWindow.navigateTo(page: 2, subTab: 0)
        consoleWindow.presentWindow()
    }

    func openActiveRulesDialog() {
        consoleWindow.navigateTo(page: 2, subTab: 2)
        consoleWindow.presentWindow()
    }

    func openAiLockDialog() {
        consoleWindow.navigateTo(page: 3)
        consoleWindow.presentWindow()
    }

    func openMultiNodeDialog() {
        consoleWindow.navigateTo(page: 4)
        consoleWindow.presentWindow()
    }

    func openDiagnosticsDialog(preselectNode: String = "") {
        consoleWindow.navigateTo(page: 5, subTab: -1, diagNode: preselectNode)
        consoleWindow.presentWindow()
    }

    func openSettingsDialog() {
        consoleWindow.navigateTo(page: 6)
        consoleWindow.presentWindow()
    }

    func importSchemeUri(_ uri: String) {
        let trimmed = uri.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        client.call(method: "import_url_scheme", params: ["uri": trimmed], timeoutSeconds: 45) { [weak self] ok, data, err, _ in
            guard let self else { return }
            if !ok {
                self.showAlert(
                    title: "ClashFX",
                    message: L("导入订阅链接失败: ", "Failed to import URI: ") + I18n.translateBackendText(err),
                    warning: true
                )
                return
            }
            let name = ((data as? [String: Any])?["name"] as? String) ?? ""
            self.client.call(method: "get_state")
            self.openSubscriptionsDialog()
            self.showAlert(
                title: "ClashFX",
                message: L("已成功导入并激活配置: ", "Imported and activated profile: ") + name,
                warning: false
            )
        }
    }

    func quitAll(forceRestoreProxy: Bool, headless: Bool = false) {
        if forceRestoreProxy {
            forceRestoreProxyOnQuit = true
        }
        if headless {
            headlessQuitOnTerminate = true
        }
        NSApp.terminate(nil)
    }

    func resetQuitFlags() {
        forceRestoreProxyOnQuit = false
        headlessQuitOnTerminate = false
    }

    func stopTrafficStreamForQuit() {
        stopTrafficStream(resetRates: false)
    }

    // MARK: - Hotkeys

    private func onOpenMainWindowHotkey() {
        if isMenuOpen {
            trayMenu.cancelTracking()
        }
        consoleWindow.presentWindow()
    }

    private func onToggleSystemProxyHotkey() {
        guard !snapshot.isEmpty else { return }
        let settings = (snapshot["settings"] as? [String: Any]) ?? [:]
        if (settings["claudeProxyLockEnabled"] as? Bool) ?? false {
            showAlert(
                title: "ClashFX",
                message: L("AI 专用锁定开启期间系统代理受保护不可关闭。",
                           "System Proxy is locked on while AI Proxy Lock is active."),
                warning: false
            )
            return
        }
        let next = !((settings["system_proxy_enabled"] as? Bool) ?? false)
        client.call(method: "set_system_proxy", params: ["enabled": next]) { [weak self] ok, _, err, _ in
            if !ok {
                self?.showAlert(title: "ClashFX", message: I18n.translateBackendText(err), warning: true)
            }
        }
    }

    private func onSwitchModeHotkey() {
        guard !snapshot.isEmpty else { return }
        let settings = (snapshot["settings"] as? [String: Any]) ?? [:]
        if (settings["claudeProxyLockEnabled"] as? Bool) ?? false {
            showAlert(
                title: "ClashFX",
                message: L("AI 专用锁定开启期间出站模式固定为规则模式。",
                           "Outbound mode is locked to Rule while AI Proxy Lock is active."),
                warning: false
            )
            return
        }
        let cur = ((settings["outbound_mode"] as? String) ?? "rule").lowercased()
        let next: String
        switch cur {
        case "rule": next = "global"
        case "global": next = "direct"
        default: next = "rule"
        }
        client.call(method: "set_outbound_mode", params: ["mode": next]) { [weak self] ok, _, err, _ in
            if !ok {
                self?.showAlert(title: "ClashFX", message: I18n.translateBackendText(err), warning: true)
            }
        }
    }

    // MARK: - Visual Icon & Tooltip

    private enum VisualState: Int {
        case disconnected = 0
        case idle = 1
        case systemProxy = 2
        case enhancedTun = 3
    }

    private func isTraySpeedEnabled() -> Bool {
        let settings = (snapshot["settings"] as? [String: Any]) ?? [:]
        return (settings["show_tray_speed"] as? Bool) ?? true
    }

    private func shouldStreamTraffic() -> Bool {
        let runtime = (snapshot["runtime"] as? [String: Any]) ?? [:]
        let coreRunning = (snapshot["core_running"] as? Bool) ?? false
        let apiPort = (runtime["api_port"] as? Int) ?? 0
        return connected && coreRunning && apiPort > 0 && isTraySpeedEnabled()
    }

    private func formatMenuBarSpeed(_ bytesPerSec: Int64) -> String {
        let valueBytes = max(bytesPerSec, 0)
        if valueBytes < 1000 {
            return "\(valueBytes)\(Self.speedUnits[0])"
        }
        var unitIdx = 0
        while unitIdx < Self.speedUnits.count - 1,
              Double(valueBytes) / Self.speedScales[unitIdx] >= 1000.0 {
            unitIdx += 1
        }
        var scaled = Double(valueBytes) / Self.speedScales[unitIdx]
        if scaled.rounded() >= 1000.0, unitIdx < Self.speedUnits.count - 1 {
            unitIdx += 1
            scaled = Double(valueBytes) / Self.speedScales[unitIdx]
        }
        if scaled < 9.95 {
            return String(format: "%.1f%@", scaled, Self.speedUnits[unitIdx])
        }
        return String(format: "%.0f%@", scaled.rounded(), Self.speedUnits[unitIdx])
    }

    private func formatMenuSpeedTitle() -> String {
        let upStr = formatMenuBarSpeed(liveUpRate)
        let downStr = formatMenuBarSpeed(liveDownRate)
        return L("代理速率: ", "Proxy Speed: ") + "↑ \(upStr)   ↓ \(downStr)"
    }

    private func syncTrafficStream() {
        let runtime = (snapshot["runtime"] as? [String: Any]) ?? [:]
        let apiPort = (runtime["api_port"] as? Int) ?? 0
        let secret = (runtime["api_secret"] as? String) ?? ""
        guard shouldStreamTraffic(), apiPort > 0 else {
            stopTrafficStream(resetRates: true)
            return
        }
        if trafficTask != nil && activeTrafficPort == apiPort && activeTrafficSecret == secret {
            return
        }
        startTrafficStream(apiPort: apiPort, secret: secret)
    }

    private func startTrafficStream(apiPort: Int, secret: String) {
        stopTrafficStream(resetRates: false)
        guard let url = URL(string: "http://127.0.0.1:\(apiPort)/traffic") else { return }

        activeTrafficPort = apiPort
        activeTrafficSecret = secret
        trafficBuffer.removeAll(keepingCapacity: false)

        let sessionCfg = URLSessionConfiguration.ephemeral
        sessionCfg.timeoutIntervalForRequest = 0
        sessionCfg.timeoutIntervalForResource = 0
        let session = URLSession(configuration: sessionCfg, delegate: self, delegateQueue: .main)
        trafficSession = session

        var req = URLRequest(url: url)
        if !secret.isEmpty {
            req.setValue("Bearer \(secret)", forHTTPHeaderField: "Authorization")
        }
        let task = session.dataTask(with: req)
        trafficTask = task
        task.resume()
    }

    private func stopTrafficStream(resetRates: Bool) {
        trafficReconnectWork?.cancel()
        trafficReconnectWork = nil
        trafficTask?.cancel()
        trafficTask = nil
        trafficSession?.invalidateAndCancel()
        trafficSession = nil
        trafficBuffer.removeAll(keepingCapacity: false)
        activeTrafficPort = 0
        activeTrafficSecret = ""
        if resetRates {
            liveUpRate = 0
            liveDownRate = 0
        }
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        guard session === trafficSession else { return }
        trafficBuffer.append(data)
        var gotUpdate = false
        while let nlRange = trafficBuffer.firstRange(of: Data([0x0A])) {
            let line = trafficBuffer.subdata(in: 0..<nlRange.lowerBound)
            trafficBuffer.removeSubrange(0..<nlRange.upperBound)
            guard !line.isEmpty,
                  let obj = try? JSONSerialization.jsonObject(with: line, options: []) as? [String: Any] else {
                continue
            }
            liveUpRate = (obj["up"] as? NSNumber)?.int64Value ?? 0
            liveDownRate = (obj["down"] as? NSNumber)?.int64Value ?? 0
            gotUpdate = true
        }
        if gotUpdate {
            refreshStatusBarVisual()
            if let item = liveSpeedMenuItem {
                item.title = formatMenuSpeedTitle()
            }
        }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        guard session === trafficSession else { return }
        trafficTask = nil
        trafficSession?.finishTasksAndInvalidate()
        trafficSession = nil
        activeTrafficPort = 0
        activeTrafficSecret = ""
        liveUpRate = 0
        liveDownRate = 0
        refreshStatusBarVisual()
        if let item = liveSpeedMenuItem {
            item.title = formatMenuSpeedTitle()
        }
        if shouldStreamTraffic() {
            trafficReconnectWork?.cancel()
            let work = DispatchWorkItem { [weak self] in
                self?.syncTrafficStream()
            }
            trafficReconnectWork = work
            DispatchQueue.main.asyncAfter(deadline: .now() + 1.5, execute: work)
        }
    }

    private func rebuildVisualsAndMenu(forceMenu: Bool = false) {
        let settings = (snapshot["settings"] as? [String: Any]) ?? [:]
        let coreRunning = (snapshot["core_running"] as? Bool) ?? false
        let preset = ((settings["tray_icon_preset"] as? String) ?? "default").lowercased()
        let sysProxy = (settings["system_proxy_enabled"] as? Bool) ?? false
        let enhanced = (settings["enhanced_mode"] as? Bool) ?? false
        let aiLocked = (settings["claudeProxyLockEnabled"] as? Bool) ?? false
        let profiles = (snapshot["profiles"] as? [String]) ?? []
        let armedStandby = profiles.isEmpty && !aiLocked

        var vis: VisualState = .disconnected
        if connected && !snapshot.isEmpty && coreRunning {
            if armedStandby { vis = .idle }
            else if enhanced { vis = .enhancedTun }
            else if sysProxy { vis = .systemProxy }
            else { vis = .idle }
        }

        let iconKey = "\(preset)|\(vis.rawValue)|\(aiLocked ? 1 : 0)"
        if iconKey != lastIconKey || cachedPillIcon == nil {
            lastIconKey = iconKey
            cachedPillIcon = renderStatusBarImage(preset: preset, state: vis, aiLocked: aiLocked)
        }

        syncTrafficStream()
        refreshStatusBarVisual()

        let tip = buildTooltip()
        if tip != lastTooltip {
            lastTooltip = tip
            statusItem.button?.toolTip = tip
        }

        let nextSig = buildMenuSignature()
        if !forceMenu && !menuDirty && nextSig == lastMenuSig {
            return
        }
        if isMenuOpen {
            menuDirty = true
            return
        }
        rebuildMenuContents()
    }

    private func refreshStatusBarVisual() {
        guard let btn = statusItem.button, let pillIcon = cachedPillIcon else { return }
        let showSpeed = isTraySpeedEnabled()
        if !showSpeed {
            let key = "\(lastIconKey)|0"
            if key != lastVisualKey {
                lastVisualKey = key
                statusItem.length = NSStatusItem.squareLength
                btn.image = pillIcon
            }
            return
        }

        let upText = formatMenuBarSpeed(liveUpRate)
        let downText = formatMenuBarSpeed(liveDownRate)
        let key = "\(lastIconKey)|1|\(upText)|\(downText)"
        if key == lastVisualKey {
            return
        }
        lastVisualKey = key
        statusItem.length = NSStatusItem.variableLength
        btn.image = renderStatusBarCompositeImage(pillIcon: pillIcon, upText: upText, downText: downText)
    }

    private func renderStatusBarCompositeImage(pillIcon: NSImage, upText: String, downText: String) -> NSImage {
        let font = Self.speedFont
        let baseAttrs: [NSAttributedString.Key: Any] = [.font: font]
        let sampleW = ceil(("↑ \(Self.speedSampleText)" as NSString).size(withAttributes: baseAttrs).width)
        let arrowW = max(
            ceil(("↑" as NSString).size(withAttributes: baseAttrs).width),
            ceil(("↓" as NSString).size(withAttributes: baseAttrs).width)
        )
        let upW = ceil((upText as NSString).size(withAttributes: baseAttrs).width)
        let downW = ceil((downText as NSString).size(withAttributes: baseAttrs).width)
        let textColW = max(sampleW, arrowW + 2 + max(upW, downW))

        let iconSize: CGFloat = 18
        let gap: CGFloat = 4
        let height: CGFloat = 22
        let width = iconSize + gap + textColW + 1

        // Draw direct strings + image inside flipped NSImage (never NSTextField)
        // to avoid the macOS 26+ (Tahoe) NSStatusBarButton infinite redraw CPU bug.
        let img = NSImage(size: NSSize(width: width, height: height), flipped: true) { rect in
            pillIcon.draw(
                in: NSRect(x: 0, y: (height - iconSize) / 2.0, width: iconSize, height: iconSize),
                from: .zero,
                operation: .sourceOver,
                fraction: 1.0,
                respectFlipped: true,
                hints: nil
            )
            let drawAttrs: [NSAttributedString.Key: Any] = [
                .font: font,
                .foregroundColor: NSColor.labelColor,
            ]
            let textX = iconSize + gap
            let rightEdge = rect.width - 1
            ("↑" as NSString).draw(at: NSPoint(x: textX, y: 0.5), withAttributes: drawAttrs)
            (upText as NSString).draw(at: NSPoint(x: max(textX + arrowW + 1, rightEdge - upW), y: 0.5), withAttributes: drawAttrs)
            ("↓" as NSString).draw(at: NSPoint(x: textX, y: 11.0), withAttributes: drawAttrs)
            (downText as NSString).draw(at: NSPoint(x: max(textX + arrowW + 1, rightEdge - downW), y: 11.0), withAttributes: drawAttrs)
            return true
        }
        img.isTemplate = false
        return img
    }

    private func renderStatusBarImage(preset: String, state: VisualState, aiLocked: Bool) -> NSImage {
        let size = NSSize(width: 18, height: 18)
        let resDir = client.config.resourceDir
        let menuIconsDir = (resDir as NSString).appendingPathComponent("MenuIcons")

        let isMono = (preset == "monochrome" || preset == "lightning")
        let isOcean = (preset == "ocean" || preset == "shield")

        let iconName: String
        if isMono {
            iconName = (state == .enhancedTun || state == .systemProxy)
                ? "menu-cat-lightning-solid.png" : "menu-cat-lightning-outline.png"
        } else if isOcean {
            iconName = (state == .enhancedTun) ? "menu-cat-shield.png" : "menu-cat-network.png"
        } else {
            iconName = (state == .idle || state == .disconnected)
                ? "menu-cat-outline-face.png" : "menu-cat-filled-face.png"
        }

        let bgColor: NSColor
        switch (isMono, isOcean, state) {
        case (true, _, .disconnected): bgColor = NSColor(calibratedWhite: 0.38, alpha: 0.85)
        case (true, _, .idle): bgColor = NSColor(calibratedWhite: 0.24, alpha: 0.92)
        case (true, _, .systemProxy): bgColor = NSColor(calibratedRed: 0.11, green: 0.13, blue: 0.16, alpha: 0.98)
        case (true, _, .enhancedTun): bgColor = NSColor(calibratedRed: 0.08, green: 0.10, blue: 0.12, alpha: 1.0)
        case (_, true, .disconnected): bgColor = NSColor(calibratedWhite: 0.44, alpha: 0.88)
        case (_, true, .idle): bgColor = NSColor(calibratedWhite: 0.32, alpha: 0.92)
        case (_, true, .systemProxy): bgColor = NSColor(calibratedRed: 15.0 / 255, green: 118.0 / 255, blue: 178.0 / 255, alpha: 0.98)
        case (_, true, .enhancedTun): bgColor = NSColor(calibratedRed: 12.0 / 255, green: 133.0 / 255, blue: 153.0 / 255, alpha: 1.0)
        case (_, _, .disconnected): bgColor = NSColor(calibratedWhite: 0.47, alpha: 0.85)
        case (_, _, .idle): bgColor = NSColor(calibratedWhite: 0.34, alpha: 0.92)
        case (_, _, .systemProxy): bgColor = NSColor(calibratedRed: 37.0 / 255, green: 99.0 / 255, blue: 235.0 / 255, alpha: 0.98)
        case (_, _, .enhancedTun): bgColor = NSColor(calibratedRed: 16.0 / 255, green: 185.0 / 255, blue: 129.0 / 255, alpha: 1.0)
        }

        let fgColor: NSColor
        if isMono && state == .systemProxy {
            fgColor = NSColor(calibratedRed: 116.0 / 255, green: 192.0 / 255, blue: 252.0 / 255, alpha: 1.0)
        } else if isMono && state == .enhancedTun {
            fgColor = NSColor(calibratedRed: 99.0 / 255, green: 230.0 / 255, blue: 190.0 / 255, alpha: 1.0)
        } else {
            fgColor = .white
        }

        let iconPath = (menuIconsDir as NSString).appendingPathComponent(iconName)
        let srcImg = NSImage(contentsOfFile: iconPath)
            ?? NSImage(contentsOfFile: (resDir as NSString).appendingPathComponent("menu_icon@2x.png"))

        let inset = size.width * 0.14
        let iconTargetSize = NSSize(width: size.width - inset * 2, height: size.height - inset * 2)
        let tintedIcon = srcImg.map { src in
            NSImage(size: iconTargetSize, flipped: false) { dstRect in
                src.draw(in: dstRect, from: .zero, operation: .sourceOver, fraction: 1.0)
                fgColor.setFill()
                dstRect.fill(using: .sourceAtop)
                return true
            }
        }

        let result = NSImage(size: size, flipped: false) { rect in
            let pad = rect.width * 0.04
            let pillRect = rect.insetBy(dx: pad, dy: pad)
            let path = NSBezierPath(roundedRect: pillRect, xRadius: rect.width * 0.24, yRadius: rect.height * 0.24)
            bgColor.setFill()
            path.fill()

            if let tintedIcon {
                let targetRect = rect.insetBy(dx: inset, dy: inset)
                tintedIcon.draw(in: targetRect, from: .zero, operation: .sourceOver, fraction: 1.0)
            }

            if aiLocked {
                let r = max(2.8, rect.width * 0.19)
                let dotRect = NSRect(x: rect.maxX - r * 2 - pad * 0.4, y: pad * 0.4, width: r * 2, height: r * 2)
                let dot = NSBezierPath(ovalIn: dotRect)
                NSColor(calibratedRed: 245.0 / 255, green: 158.0 / 255, blue: 11.0 / 255, alpha: 1.0).setFill()
                dot.fill()
                NSColor.white.setStroke()
                dot.lineWidth = 1.0
                dot.stroke()
            }
            return true
        }
        result.isTemplate = false
        return result
    }

    private func buildTooltip() -> String {
        if !connected || snapshot.isEmpty {
            return L("ClashFX — 正在连接后台守护进程...", "ClashFX — Connecting to background daemon...")
        }
        let settings = (snapshot["settings"] as? [String: Any]) ?? [:]
        let runtime = (snapshot["runtime"] as? [String: Any]) ?? [:]
        let coreRunning = (snapshot["core_running"] as? Bool) ?? false
        let profiles = (snapshot["profiles"] as? [String]) ?? []
        let profileLabel = profiles.isEmpty
            ? L("未导入订阅", "No Subscription")
            : ((settings["selected_profile"] as? String) ?? profiles[0])
        let mode = ((settings["outbound_mode"] as? String) ?? "rule").uppercased()
        let mixedPort = (runtime["mixed_port"] as? Int) ?? 7890
        let sysProxy = (settings["system_proxy_enabled"] as? Bool) ?? false
        let enhanced = (settings["enhanced_mode"] as? Bool) ?? false
        let aiLocked = (settings["claudeProxyLockEnabled"] as? Bool) ?? false
        let aiTarget = (settings["claudeProxyLockTarget"] as? String) ?? ""
        let suspendedNet = (snapshot["suspended_by_network"] as? String) ?? ""
        let arch = (snapshot["arch"] as? String) ?? ""

        var lines = ["ClashFX (\(arch))"]
        if !coreRunning {
            lines.append(L("状态: 内核未运行 (请检查配置)", "Status: Core Stopped (Check Config)"))
        }
        lines.append(L("配置: ", "Profile: ") + profileLabel + "  |  " + L("模式: ", "Mode: ") + mode)
        lines.append(L("混合端口: ", "Mixed Port: ") + "\(mixedPort)")
        if !suspendedNet.isEmpty {
            lines.append(L("系统代理: 已自动暂停 (", "System Proxy: Paused (") + suspendedNet + ")")
        } else {
            let armedStandby = profiles.isEmpty && !aiLocked
            let onLabel = armedStandby ? L("待导入订阅", "Pending Sub") : L("已开启", "On")
            lines.append(
                L("系统代理: ", "System Proxy: ") + (sysProxy ? onLabel : L("已关闭", "Off"))
                + "  |  TUN: " + (enhanced ? onLabel : L("已关闭", "Off"))
            )
        }
        if aiLocked {
            lines.append(L("AI 专用锁定: ", "AI Proxy Lock: ") + aiTarget)
        }
        if isTraySpeedEnabled() {
            lines.append(L("仅显示经过 ClashFX 本地代理或增强模式的流量，不代表电脑整体网速。",
                           "Shows traffic that passes through ClashFX's local proxy or Enhanced Mode, not total system traffic."))
        }
        return lines.joined(separator: "\n")
    }

    // MARK: - NSMenu Construction

    private func buildMenuSignature() -> String {
        let settings = (snapshot["settings"] as? [String: Any]) ?? [:]
        let runtime = (snapshot["runtime"] as? [String: Any]) ?? [:]
        let sigObj: [String: Any] = [
            "connected": connected,
            "core_running": (snapshot["core_running"] as? Bool) ?? false,
            "lang": I18n.shared.language,
            "preset": settings["tray_icon_preset"] ?? "default",
            "show_speed": (settings["show_tray_speed"] as? Bool) ?? true,
            "sys": settings["system_proxy_enabled"] ?? false,
            "tun": settings["enhanced_mode"] ?? false,
            "ai_en": settings["claudeProxyLockEnabled"] ?? false,
            "ai_tgt": settings["claudeProxyLockTarget"] ?? "",
            "mode": settings["outbound_mode"] ?? "rule",
            "prof": settings["selected_profile"] ?? "config",
            "allow_lan": settings["allow_lan"] ?? false,
            "ipv6": settings["ipv6"] ?? false,
            "bypass_cn": settings["bypass_cn_apps"] ?? false,
            "as_is": settings["custom_config_as_is"] ?? false,
            "autostart": settings["launch_at_login"] ?? false,
            "mixed_port": runtime["mixed_port"] ?? 7890,
            "profiles": snapshot["profiles"] ?? [],
            "groups": snapshot["proxy_groups"] ?? [],
            "sub_info": snapshot["active_subscription_info"] ?? [:],
            "custom_rules": settings["custom_rules"] ?? [],
            "hk_open": resolveOpenMainWindowChord(settings),
        ]
        guard let data = try? JSONSerialization.data(withJSONObject: sigObj, options: [.sortedKeys]),
              let s = String(data: data, encoding: .utf8) else {
            return ""
        }
        return s
    }

    @discardableResult
    private func addItem(
        to menu: NSMenu,
        title: String,
        enabled: Bool = true,
        checked: Bool = false,
        bold: Bool = false,
        action: (() -> Void)? = nil
    ) -> NSMenuItem {
        let item = NSMenuItem(title: title, action: action != nil ? #selector(onMenuItemInvoked(_:)) : nil, keyEquivalent: "")
        item.target = self
        item.isEnabled = enabled
        item.state = checked ? .on : .off
        if bold {
            item.attributedTitle = NSAttributedString(
                string: title,
                attributes: [.font: NSFont.boldSystemFont(ofSize: NSFont.systemFontSize)]
            )
        }
        if let action {
            let tag = nextTag
            nextTag += 1
            item.tag = tag
            actionMap[tag] = action
        }
        menu.addItem(item)
        return item
    }

    @objc private func onMenuItemInvoked(_ sender: NSMenuItem) {
        actionMap[sender.tag]?()
    }

    private func rebuildMenuContents() {
        actionMap.removeAll()
        nextTag = 100
        liveSpeedMenuItem = nil
        trayMenu.removeAllItems()

        let settings = (snapshot["settings"] as? [String: Any]) ?? [:]
        let runtime = (snapshot["runtime"] as? [String: Any]) ?? [:]
        let coreRunning = (snapshot["core_running"] as? Bool) ?? false
        let preset = (settings["tray_icon_preset"] as? String) ?? "default"
        let showSpeed = (settings["show_tray_speed"] as? Bool) ?? true
        let sysProxy = (settings["system_proxy_enabled"] as? Bool) ?? false
        let enhanced = (settings["enhanced_mode"] as? Bool) ?? false
        let aiLocked = (settings["claudeProxyLockEnabled"] as? Bool) ?? false
        let aiTarget = (settings["claudeProxyLockTarget"] as? String) ?? ""
        let mode = ((settings["outbound_mode"] as? String) ?? "rule").lowercased()
        let selProfile = (settings["selected_profile"] as? String) ?? "config"
        let mixedPort = (runtime["mixed_port"] as? Int) ?? 7890

        lastMenuSig = buildMenuSignature()
        menuDirty = false

        if connected && !snapshot.isEmpty && !coreRunning {
            addItem(
                to: trayMenu,
                title: L("⚠ 代理内核未运行（点击打开控制台查看原因）", "⚠ Proxy core stopped (Click to inspect)"),
                enabled: true
            ) { [weak self] in
                self?.openConsole()
            }
            trayMenu.addItem(.separator())
        }

        if showSpeed && coreRunning {
            liveSpeedMenuItem = addItem(to: trayMenu, title: formatMenuSpeedTitle(), enabled: false)
        }

        if let subInfo = snapshot["active_subscription_info"] as? [String: Any] {
            let line = buildTopSubscriptionStatusText(profileName: selProfile, subInfo: subInfo)
            if !line.isEmpty {
                addItem(to: trayMenu, title: line, enabled: false)
            }
        }
        if trayMenu.numberOfItems > 0 && !((trayMenu.items.last?.isSeparatorItem) ?? true) {
            trayMenu.addItem(.separator())
        }

        let openChord = resolveOpenMainWindowChord(settings)
        var openTitle = L("打开控制台 (MetaCubeXD)", "Open Console (MetaCubeXD)")
        if !openChord.isEmpty {
            openTitle += "  [\(openChord)]"
        }
        addItem(to: trayMenu, title: openTitle, bold: true) { [weak self] in
            self?.openConsole()
        }
        trayMenu.addItem(.separator())

        // Outbound Mode
        var modeTitle = L("出站模式", "Outbound Mode") + " (\(mode.uppercased()))"
        if aiLocked {
            modeTitle += L(" [AI 锁定中]", " [AI Locked]")
        }
        let modeSub = NSMenu()
        for (val, label) in [
            ("rule", L("规则判断 (Rule)", "Rule")),
            ("global", L("全局连接 (Global)", "Global")),
            ("direct", L("直接连接 (Direct)", "Direct")),
        ] {
            addItem(to: modeSub, title: label, enabled: connected && !aiLocked, checked: mode == val) { [weak self] in
                self?.client.call(method: "set_outbound_mode", params: ["mode": val]) { ok, _, err, _ in
                    if !ok {
                        self?.showAlert(title: "ClashFX", message: I18n.translateBackendText(err), warning: true)
                    }
                }
            }
        }
        let modeMenuItem = NSMenuItem(title: modeTitle, action: nil, keyEquivalent: "")
        modeMenuItem.submenu = modeSub
        trayMenu.addItem(modeMenuItem)

        // Dynamic Proxy Groups
        let rawGroups = (snapshot["proxy_groups"] as? [[String: Any]]) ?? []
        let orderedGroups = rawGroups.sorted { a, b in
            proxyGroupSortTier((a["type"] as? String) ?? "") < proxyGroupSortTier((b["type"] as? String) ?? "")
        }
        var addedGroupsSep = false
        for g in orderedGroups {
            let gName = (g["name"] as? String) ?? ""
            let gType = (g["type"] as? String) ?? ""
            let now = (g["now"] as? String) ?? ""
            let hasManual = (g["has_manual_override"] as? Bool) ?? false

            if gName == "GLOBAL" && mode != "global" { continue }
            if !addedGroupsSep {
                trayMenu.addItem(.separator())
                addedGroupsSep = true
            }

            let isLB = gType.caseInsensitiveCompare("LoadBalance") == .orderedSame
                || gType.caseInsensitiveCompare("load-balance") == .orderedSame
            let isRelay = gType.caseInsensitiveCompare("Relay") == .orderedSame
            let isAutoGroup = gType.caseInsensitiveCompare("URLTest") == .orderedSame
                || gType.caseInsensitiveCompare("url-test") == .orderedSame
                || gType.caseInsensitiveCompare("Fallback") == .orderedSame
                || gType.caseInsensitiveCompare("fallback") == .orderedSame
            var gTitle = gName
            if !now.isEmpty {
                gTitle += "  [\(now)]"
            } else if isLB {
                gTitle += "  [\(L("负载均衡", "LoadBalance"))]"
            }
            if hasManual {
                gTitle += L(" [固定]", " [Pinned]")
            }

            let gSub = NSMenu()
            addItem(to: gSub, title: L("测速本组节点", "Benchmark Group"), enabled: connected && coreRunning) { [weak self] in
                self?.client.call(method: "benchmark_group", params: ["group": gName], timeoutSeconds: 45)
            }
            gSub.addItem(.separator())

            let nodes = (g["items"] as? [[String: Any]]) ?? []
            for n in nodes {
                let nName = (n["name"] as? String) ?? ""
                let delay = (n["delay_ms"] as? Int) ?? 0
                let tested = (n["tested"] as? Bool) ?? false
                let isSelected = (!now.isEmpty && nName == now)

                var label = nName
                if delay > 0 {
                    label += "  (\(delay) ms)"
                } else if tested && nName != "DIRECT" && nName != "REJECT" {
                    label += L("  (超时)", "  (Timeout)")
                }

                if isLB || isRelay {
                    addItem(to: gSub, title: label, enabled: false, checked: isSelected)
                } else {
                    addItem(to: gSub, title: label, enabled: connected, checked: isSelected) { [weak self] in
                        self?.client.call(method: "select_proxy", params: ["group": gName, "target": nName]) { ok, _, err, _ in
                            if !ok {
                                self?.showAlert(title: "ClashFX", message: I18n.translateBackendText(err), warning: true)
                            }
                        }
                    }
                }
            }

            let showDiag = (!isLB && !now.isEmpty && now != "DIRECT" && now != "REJECT")
            if isAutoGroup || showDiag {
                gSub.addItem(.separator())
                if isAutoGroup {
                    if hasManual {
                        addItem(to: gSub, title: L("恢复自动选择", "Restore Automatic Selection"), enabled: connected) { [weak self] in
                            self?.client.call(method: "restore_auto_selection", params: ["group": gName]) { ok, _, err, _ in
                                if !ok {
                                    self?.showAlert(title: "ClashFX", message: I18n.translateBackendText(err), warning: true)
                                }
                            }
                        }
                    } else {
                        addItem(to: gSub, title: L("自动测速择优中", "Automatic Selection Active"), enabled: false)
                    }
                }
                if showDiag {
                    addItem(to: gSub, title: L("诊断当前节点…", "Diagnose Current Node…")) { [weak self] in
                        self?.openDiagnosticsDialog(preselectNode: now)
                    }
                }
            }

            let gItem = NSMenuItem(title: gTitle, action: nil, keyEquivalent: "")
            gItem.submenu = gSub
            trayMenu.addItem(gItem)
        }

        trayMenu.addItem(.separator())

        if aiLocked {
            addItem(
                to: trayMenu,
                title: L("已由 AI 专用锁定接管代理开关与规则模式", "Proxy & Rule mode managed by AI Proxy Lock"),
                enabled: false
            )
        }

        // Core proxy switches
        addItem(to: trayMenu, title: L("设置为系统代理", "Set as System Proxy"), enabled: connected && !aiLocked, checked: sysProxy) { [weak self] in
            self?.client.call(method: "set_system_proxy", params: ["enabled": !sysProxy]) { ok, _, err, _ in
                if !ok {
                    self?.showAlert(title: "ClashFX", message: I18n.translateBackendText(err), warning: true)
                }
            }
        }
        addItem(to: trayMenu, title: L("增强模式 (TUN)", "Enhanced Mode (TUN)"), enabled: connected && !aiLocked, checked: enhanced) { [weak self] in
            self?.client.call(method: "set_enhanced_mode", params: ["enabled": !enhanced, "allow_uac": true], timeoutSeconds: 45) { ok, _, err, _ in
                if !ok {
                    self?.showAlert(title: "ClashFX", message: I18n.translateBackendText(err), warning: true)
                }
            }
        }
        addItem(to: trayMenu, title: L("关闭所有代理模式", "Turn Off All Proxy Modes"), enabled: connected && !aiLocked) { [weak self] in
            self?.client.call(method: "turn_off_all_proxies") { ok, _, err, _ in
                if !ok {
                    self?.showAlert(title: "ClashFX", message: I18n.translateBackendText(err), warning: true)
                }
            }
        }

        trayMenu.addItem(.separator())

        // Exclusive features
        var aiTitle = L("AI 专用锁定…", "AI Proxy Lock…")
        if aiLocked && !aiTarget.isEmpty {
            aiTitle += "  [\(aiTarget)]"
        }
        addItem(to: trayMenu, title: aiTitle) { [weak self] in self?.openAiLockDialog() }
        addItem(to: trayMenu, title: L("多节点固定端口代理…", "Multiple Node Proxies…")) { [weak self] in self?.openMultiNodeDialog() }

        trayMenu.addItem(.separator())

        // Config submenu
        let cfgSub = NSMenu()
        let profiles = (snapshot["profiles"] as? [String]) ?? []
        if profiles.isEmpty {
            addItem(to: cfgSub, title: L("暂无已导入订阅", "No Subscriptions"), enabled: false) {}
        } else {
            for pName in profiles {
                addItem(to: cfgSub, title: pName, enabled: true, checked: pName == selProfile) { [weak self] in
                    self?.client.call(method: "select_profile", params: ["name": pName]) { ok, _, err, _ in
                        self?.handleConfigMutationResult(
                            actionTitle: L("切换配置", "Switch Profile"),
                            retryAction: { self?.client.call(method: "select_profile", params: ["name": pName]) },
                            ok: ok,
                            err: err
                        )
                    }
                }
            }
        }
        cfgSub.addItem(.separator())
        addItem(to: cfgSub, title: L("重载当前配置", "Reload Config")) { [weak self] in
            self?.client.call(method: "reload_config", params: ["allow_uac": true], timeoutSeconds: 120) { ok, _, err, _ in
                self?.handleConfigMutationResult(
                    actionTitle: L("重载当前配置", "Reload Config"),
                    retryAction: { self?.client.call(method: "reload_config", params: ["allow_uac": true], timeoutSeconds: 120) },
                    ok: ok,
                    err: err
                )
            }
        }
        addItem(to: cfgSub, title: L("配置补丁 (Profile Mixin)…", "Config Patch (Profile Mixin)…")) { [weak self] in
            self?.openRuleEditorDialog()
        }
        addItem(to: cfgSub, title: L("打开配置文件夹", "Open Config Folder")) { [weak self] in
            if let dir = self?.snapshot["config_dir"] as? String, !dir.isEmpty {
                NSWorkspace.shared.open(URL(fileURLWithPath: dir))
            }
        }
        addItem(to: cfgSub, title: L("托管订阅管理…", "Managed Subscriptions…")) { [weak self] in
            self?.openSubscriptionsDialog()
        }
        addItem(to: cfgSub, title: L("更新 GEO 数据库 (MMDB / GeoSite)", "Update GEO Databases")) { [weak self] in
            self?.client.call(method: "update_geo_databases", timeoutSeconds: 60) { ok, _, err, _ in
                self?.showAlert(
                    title: "ClashFX",
                    message: ok ? L("GEO 数据库更新完成并已热重载。", "GEO databases updated and hot-reloaded.")
                                : I18n.translateBackendText(err),
                    warning: !ok
                )
            }
        }
        let cfgMenuTitle = profiles.isEmpty
            ? L("配置管理", "Config")
            : L("配置管理", "Config") + " (\(selProfile))"
        let cfgItem = NSMenuItem(title: cfgMenuTitle, action: nil, keyEquivalent: "")
        cfgItem.submenu = cfgSub
        trayMenu.addItem(cfgItem)

        // Speed test & Copy terminal command
        addItem(to: trayMenu, title: L("一键延迟测速", "Latency Speed Test"), enabled: connected && coreRunning) { [weak self] in
            self?.client.call(method: "benchmark_all", timeoutSeconds: 60) { ok, _, err, _ in
                if !ok {
                    self?.showAlert(title: "ClashFX", message: I18n.translateBackendText(err), warning: true)
                }
            }
        }

        let copyCmdSub = NSMenu()
        addItem(to: copyCmdSub, title: "zsh / bash") {
            let cmd = "export https_proxy=http://127.0.0.1:\(mixedPort) http_proxy=http://127.0.0.1:\(mixedPort) all_proxy=socks5://127.0.0.1:\(mixedPort)"
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(cmd, forType: .string)
        }
        addItem(to: copyCmdSub, title: "fish") {
            let cmd = "set -gx https_proxy http://127.0.0.1:\(mixedPort); set -gx http_proxy http://127.0.0.1:\(mixedPort); set -gx all_proxy socks5://127.0.0.1:\(mixedPort)"
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(cmd, forType: .string)
        }
        let copyCmdItem = NSMenuItem(title: L("复制终端代理命令", "Copy Terminal Proxy Command"), action: nil, keyEquivalent: "")
        copyCmdItem.submenu = copyCmdSub
        trayMenu.addItem(copyCmdItem)

        // Settings submenu
        let settingsSub = NSMenu()
        addItem(to: settingsSub, title: L("偏好设置…", "Preferences…")) { [weak self] in self?.openSettingsDialog() }
        settingsSub.addItem(.separator())

        let toggles: [(String, String, Bool)] = [
            (L("显示 ClashFX 代理速率", "Show Proxy Speed"), "show_tray_speed", true),
            (L("允许局域网连接 (Allow LAN)", "Allow LAN"), "allow_lan", false),
            (L("启用 IPv6", "Enable IPv6"), "ipv6", false),
            (L("绕过常用国内应用", "Bypass Common Chinese Apps"), "bypass_cn_apps", false),
            (L("增强模式按原样使用配置", "Use Custom Config as-is"), "custom_config_as_is", false),
            (L("开机自动启动", "Launch at Login"), "launch_at_login", false),
        ]
        for (title, key, defaultVal) in toggles {
            let cur = (settings[key] as? Bool) ?? defaultVal
            addItem(to: settingsSub, title: title, enabled: connected, checked: cur) { [weak self] in
                self?.mutateSetting(key: key, value: !cur)
            }
        }
        settingsSub.addItem(.separator())

        let iconStyleSub = NSMenu()
        for (val, label) in [
            ("default", L("经典猫咪 (Default)", "Default Cat")),
            ("monochrome", L("极简闪电 (Monochrome)", "Monochrome Lightning")),
            ("ocean", L("海洋护盾 (Ocean)", "Ocean Shield")),
        ] {
            addItem(
                to: iconStyleSub,
                title: label,
                enabled: connected,
                checked: preset.caseInsensitiveCompare(val) == .orderedSame
            ) { [weak self] in
                self?.mutateSetting(key: "tray_icon_preset", value: val)
            }
        }
        let iconStyleItem = NSMenuItem(title: L("托盘图标风格", "Tray Icon Style"), action: nil, keyEquivalent: "")
        iconStyleItem.submenu = iconStyleSub
        settingsSub.addItem(iconStyleItem)

        let langSub = NSMenu()
        for (code, label) in [("zh_CN", "简体中文"), ("en_US", "English")] {
            addItem(to: langSub, title: label, enabled: true, checked: I18n.shared.language == code) { [weak self] in
                I18n.shared.setLanguage(code)
                self?.mutateSetting(key: "language", value: code)
            }
        }
        let langItem = NSMenuItem(title: L("语言 / Language", "Language / 语言"), action: nil, keyEquivalent: "")
        langItem.submenu = langSub
        settingsSub.addItem(langItem)

        let settingsMenuItem = NSMenuItem(title: L("设置", "Settings"), action: nil, keyEquivalent: "")
        settingsMenuItem.submenu = settingsSub
        trayMenu.addItem(settingsMenuItem)

        // Help submenu
        let helpSub = NSMenu()
        addItem(to: helpSub, title: L("单节点测速诊断…", "Single-Node Diagnostic…")) { [weak self] in self?.openDiagnosticsDialog() }
        addItem(to: helpSub, title: L("复制脱敏诊断报告", "Copy Redacted Diagnostic Info")) { [weak self] in
            self?.copyDiagnosticInfo()
        }
        addItem(to: helpSub, title: L("打开日志文件夹", "Open Logs Folder")) { [weak self] in
            if let dir = self?.snapshot["logs_dir"] as? String, !dir.isEmpty {
                try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
                NSWorkspace.shared.open(URL(fileURLWithPath: dir))
            }
        }
        addItem(to: helpSub, title: L("检查版本更新…", "Check for Updates…")) { [weak self] in self?.checkForUpdates() }
        addItem(to: helpSub, title: L("关于 ClashFX", "About ClashFX")) { [weak self] in self?.showAboutBox() }
        let helpMenuItem = NSMenuItem(title: L("帮助与诊断", "Help & Diagnostics"), action: nil, keyEquivalent: "")
        helpMenuItem.submenu = helpSub
        trayMenu.addItem(helpMenuItem)

        trayMenu.addItem(.separator())
        addItem(to: trayMenu, title: L("退出 ClashFX", "Quit ClashFX")) { [weak self] in
            self?.quitAll(forceRestoreProxy: false)
        }
    }

    // MARK: - Helpers

    private func proxyGroupSortTier(_ type: String) -> Int {
        if type.caseInsensitiveCompare("Selector") == .orderedSame
            || type.caseInsensitiveCompare("Select") == .orderedSame {
            return 0
        }
        if type.caseInsensitiveCompare("URLTest") == .orderedSame
            || type.caseInsensitiveCompare("Fallback") == .orderedSame {
            return 1
        }
        return 2
    }

    private func formatTrafficBytes(_ bytes: Int64) -> String {
        if bytes <= 0 { return "0 B" }
        let gb = Double(bytes) / (1024.0 * 1024.0 * 1024.0)
        if gb >= 1.0 { return String(format: "%.2f GB", gb) }
        let mb = Double(bytes) / (1024.0 * 1024.0)
        return String(format: "%.1f MB", mb)
    }

    private func buildTopSubscriptionStatusText(profileName: String, subInfo: [String: Any]) -> String {
        if subInfo.isEmpty { return "" }
        var quotaPart = ""
        if subInfo["upload"] != nil || subInfo["download"] != nil || subInfo["total"] != nil {
            let up = (subInfo["upload"] as? NSNumber)?.int64Value ?? 0
            let down = (subInfo["download"] as? NSNumber)?.int64Value ?? 0
            let total = (subInfo["total"] as? NSNumber)?.int64Value ?? 0
            let used = max(0, up + down)
            if total > 0 {
                quotaPart = "\(formatTrafficBytes(used)) / \(formatTrafficBytes(total))"
            } else if used > 0 {
                quotaPart = formatTrafficBytes(used)
            }
        }
        var expirePart = ""
        let expTs = (subInfo["expire"] as? NSNumber)?.int64Value ?? 0
        let expText = ((subInfo["expire_text"] as? String) ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        if expTs > 0 {
            let df = DateFormatter()
            df.dateFormat = "yyyy-MM-dd"
            expirePart = L("到期 ", "Exp ") + df.string(from: Date(timeIntervalSince1970: TimeInterval(expTs)))
        } else if !expText.isEmpty {
            expirePart = L("到期 ", "Exp ") + expText
        }
        if quotaPart.isEmpty && expirePart.isEmpty { return "" }
        var parts: [String] = []
        if !profileName.isEmpty { parts.append(profileName) }
        if !quotaPart.isEmpty { parts.append(quotaPart) }
        if !expirePart.isEmpty { parts.append(expirePart) }
        return parts.joined(separator: " · ")
    }

    private func mutateSetting(key: String, value: Any) {
        guard var settings = snapshot["settings"] as? [String: Any] else { return }
        settings[key] = value
        client.call(method: "update_settings", params: ["settings": settings]) { [weak self] ok, _, err, _ in
            if !ok {
                self?.showAlert(title: "ClashFX", message: I18n.translateBackendText(err), warning: true)
            }
        }
    }

    private func handleConfigMutationResult(
        actionTitle: String,
        retryAction: (() -> Void)?,
        ok: Bool,
        err: String
    ) {
        if ok { return }
        let localizedErr = I18n.translateBackendText(err)
        let settings = (snapshot["settings"] as? [String: Any]) ?? [:]
        let aiLocked = (settings["claudeProxyLockEnabled"] as? Bool) ?? false

        if aiLocked, let retryAction {
            let alert = NSAlert()
            alert.alertStyle = .warning
            alert.messageText = actionTitle
            alert.informativeText = actionTitle
                + L("失败：\n", " failed:\n") + localizedErr
                + L("\n\n当前已启用 AI 专用锁定（要求目标配置包含锁定的具体节点）。是否停用 AI 专用锁定并立即重试？",
                    "\n\nAI Proxy Lock is active (which requires the locked node to exist in the profile). Disable AI Proxy Lock and retry now?")
            alert.addButton(withTitle: L("停用 AI 锁定并重试", "Disable AI Lock & Retry"))
            alert.addButton(withTitle: L("取消", "Cancel"))
            NSApp.activate(ignoringOtherApps: true)
            if alert.runModal() == .alertFirstButtonReturn {
                client.call(
                    method: "set_ai_lock",
                    params: ["enabled": false, "target": "", "allow_uac": false]
                ) { [weak self] disOk, _, disErr, _ in
                    if !disOk {
                        self?.showAlert(title: actionTitle, message: I18n.translateBackendText(disErr), warning: true)
                        return
                    }
                    retryAction()
                }
            }
            return
        }
        showAlert(title: "ClashFX", message: "\(actionTitle): \(localizedErr)", warning: true)
    }

    private func copyDiagnosticInfo() {
        client.call(method: "get_diagnostic_info") { [weak self] ok, data, err, _ in
            guard let self else { return }
            if !ok {
                self.showAlert(title: "ClashFX", message: I18n.translateBackendText(err), warning: true)
                return
            }
            let report = ((data as? [String: Any])?["report"] as? String) ?? ""
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(report, forType: .string)
            self.showAlert(
                title: "ClashFX",
                message: L("脱敏诊断信息已复制到剪贴板。", "Redacted diagnostic info copied to clipboard."),
                warning: false
            )
        }
    }

    private func checkForUpdates() {
        client.call(method: "check_updates", timeoutSeconds: 30) { [weak self] ok, data, err, _ in
            guard let self else { return }
            if !ok {
                self.showAlert(title: L("检查更新", "Check for Updates"), message: I18n.translateBackendText(err), warning: true)
                return
            }
            let res = (data as? [String: Any]) ?? [:]
            let curVer = (res["current_version"] as? String) ?? ""
            let avail = (res["update_available"] as? Bool) ?? false
            let latest = (res["latest_version"] as? String) ?? ""
            let dl = (res["download_url"] as? String) ?? ""
            let rel = (res["release_url"] as? String) ?? ""
            let urlStr = dl.isEmpty ? rel : dl

            if !avail {
                self.showAlert(
                    title: L("检查更新", "Check for Updates"),
                    message: L("当前已是最新版本 (", "You are running the latest version (") + curVer + ").",
                    warning: false
                )
                return
            }

            let alert = NSAlert()
            alert.messageText = L("发现新版本", "Update Available")
            alert.informativeText = L("发现新版本 ", "New version available: ") + latest
                + L("\n是否立即在浏览器打开下载页面？", "\nOpen download page in browser now?")
            alert.addButton(withTitle: L("打开下载页面", "Open Download Page"))
            alert.addButton(withTitle: L("取消", "Cancel"))
            NSApp.activate(ignoringOtherApps: true)
            if alert.runModal() == .alertFirstButtonReturn, let url = URL(string: urlStr) {
                NSWorkspace.shared.open(url)
            }
        }
    }

    private func showAboutBox() {
        let ver = (snapshot["version"] as? String) ?? "1.19.24"
        let coreVer = (snapshot["core_version"] as? String).flatMap { $0.isEmpty ? nil : $0 } ?? "v1.19.24"
        let arch = (snapshot["arch"] as? String) ?? ""
        showAlert(
            title: L("关于 ClashFX", "About ClashFX"),
            message: "ClashFX for macOS\nVersion: \(ver) (\(arch))\nAppKit + WKWebView + Mihomo Core \(coreVer)\nFeatures: AI Proxy Lock, Multi-Node Fixed Ports & 204 Recovery, Privileged TUN Daemon.",
            warning: false
        )
    }

    private func showAlert(title: String, message: String, warning: Bool) {
        let alert = NSAlert()
        alert.alertStyle = warning ? .warning : .informational
        alert.messageText = title
        alert.informativeText = message
        alert.addButton(withTitle: L("确定", "OK"))
        NSApp.activate(ignoringOtherApps: true)
        alert.runModal()
    }
}
