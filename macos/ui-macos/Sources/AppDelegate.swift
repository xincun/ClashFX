import Cocoa
import Darwin
import Foundation

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var config = DaemonConfig.resolveDefault()
    private var backgroundStart = false
    private var startupCmd = ""
    private var startupUri = ""
    private var smokeTestMs = 0
    private var smokeReportPath = ""

    private var daemonClient: DaemonClient?
    private var consoleWindow: ConsoleWindowController?
    private var trayController: TrayController?
    private var wakeObserver: NSObjectProtocol?
    private var powerOffObserver: NSObjectProtocol?
    private var instanceLockFd: Int32 = -1
    private var controlListenFd: Int32 = -1
    private var controlSocketPath = ""
    private var isTerminating = false
    private var terminationCompleted = false
    private var poweringOff = false
    private var isPromptingQuit = false
    private var quitPromptConfirmed = false
    private var killAiClientsOnQuit = false

    override init() {
        super.init()
        parseArguments()
    }

    func applicationWillFinishLaunching(_ notification: Notification) {
        NSAppleEventManager.shared().setEventHandler(
            self,
            andSelector: #selector(handleGetURLEvent(_:withReplyEvent:)),
            forEventClass: AEEventClass(kInternetEventClass),
            andEventID: AEEventID(kAEGetURL)
        )
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        if !acquireSingleInstanceOrForward() {
            exit(0)
        }
        if startupCmd == "--quit" || startupCmd == "--quit-uninstall" {
            let tempClient = DaemonClient(config: config)
            _ = tempClient.sendQuitDaemonSync(forceRestoreProxy: startupCmd == "--quit-uninstall", timeoutSeconds: 2)
            stopControlSocket()
            if instanceLockFd >= 0 {
                Darwin.close(instanceLockFd)
                instanceLockFd = -1
            }
            exit(0)
        }
        installStandardMenus()

        let client = DaemonClient(config: config)
        let showInitially = (!backgroundStart || smokeTestMs > 0) && startupCmd != "--quit" && startupCmd != "--quit-uninstall"
        let console = ConsoleWindowController(client: client)
        let tray = TrayController(client: client, consoleWindow: console, showConsoleInitially: showInitially)

        self.daemonClient = client
        self.consoleWindow = console
        self.trayController = tray

        client.start()

        wakeObserver = NSWorkspace.shared.notificationCenter.addObserver(
            forName: NSWorkspace.didWakeNotification,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            self?.daemonClient?.call(method: "power_resume")
        }

        powerOffObserver = NSWorkspace.shared.notificationCenter.addObserver(
            forName: NSWorkspace.willPowerOffNotification,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            self?.poweringOff = true
        }

        if !startupUri.isEmpty {
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.7) { [weak tray] in
                tray?.handleExternalCommand("URI:\(self.startupUri)")
            }
        }
        if !startupCmd.isEmpty {
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) { [weak tray] in
                tray?.handleExternalCommand(self.startupCmd)
            }
        }
        if smokeTestMs > 0 {
            scheduleSmokeTest()
        }
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        if !flag {
            trayController?.openConsole()
        }
        return true
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        if terminationCompleted {
            return .terminateNow
        }
        guard let client = daemonClient else {
            consoleWindow?.setAllowClose(true)
            terminationCompleted = true
            return .terminateNow
        }
        if isTerminating || isPromptingQuit {
            return .terminateLater
        }

        let settings = (client.snapshot["settings"] as? [String: Any]) ?? [:]
        let aiLocked = (settings["claudeProxyLockEnabled"] as? Bool) ?? false
        let forceRestore = trayController?.forceRestoreProxyOnQuit ?? false
        let headless = (trayController?.headlessQuitOnTerminate ?? false) || poweringOff || smokeTestMs > 0

        if aiLocked && client.isConnected && !forceRestore && !headless && !quitPromptConfirmed {
            isPromptingQuit = true
            client.call(method: "list_running_ai_clients", timeoutSeconds: 2) { [weak self] ok, data, _, _ in
                guard let self else { return }
                var names: [String] = []
                if ok, let dict = data as? [String: Any], let rawNames = dict["names"] as? [String] {
                    names = rawNames.filter { !$0.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
                }

                let alert = NSAlert()
                alert.alertStyle = .warning
                alert.messageText = L("退出 ClashFX（AI 专用锁定已开启）", "Quit ClashFX (AI Proxy Lock Active)")
                if !names.isEmpty {
                    let zhList = names.joined(separator: "、")
                    let enList = names.joined(separator: ", ")
                    alert.informativeText = L(
                        "检测到正在运行的受保护 AI 工具：\(zhList)。\n\n退出 ClashFX 将自动关闭系统代理并恢复本机直连上网。是否先关闭这些 AI 工具，防止它们在退出后直连窜网？",
                        "Running protected AI tools detected: \(enList).\n\nQuitting ClashFX will turn off System Proxy and restore direct network access. Close these AI tools first so they cannot reconnect directly?"
                    )
                } else {
                    alert.informativeText = L(
                        "当前已开启 AI 专用锁定（暂未检测到运行中的受保护 AI 工具）。\n\n退出 ClashFX 将自动关闭系统代理并恢复本机直连上网。是否在退出前仍检查并关闭 AI 工具以防窜网？",
                        "AI Proxy Lock is active (no running protected AI tools currently detected).\n\nQuitting ClashFX will turn off System Proxy and restore direct network access. Check and close any AI tools before exiting?"
                    )
                }
                alert.addButton(withTitle: L("关闭 AI 工具并退出", "Close AI Tools & Quit"))
                alert.addButton(withTitle: L("仅退出 ClashFX", "Quit ClashFX Only"))
                alert.addButton(withTitle: L("取消", "Cancel"))

                NSApp.activate(ignoringOtherApps: true)
                let choice = alert.runModal()
                self.isPromptingQuit = false

                if choice == .alertThirdButtonReturn {
                    self.trayController?.resetQuitFlags()
                    NSApp.reply(toApplicationShouldTerminate: false)
                    return
                }
                self.killAiClientsOnQuit = (choice == .alertFirstButtonReturn)
                self.quitPromptConfirmed = true
                self.proceedWithDaemonQuit(client: client)
            }
            return .terminateLater
        }

        proceedWithDaemonQuit(client: client)
        return .terminateLater
    }

    private func proceedWithDaemonQuit(client: DaemonClient) {
        if isTerminating {
            return
        }
        isTerminating = true
        trayController?.stopTrafficStreamForQuit()
        consoleWindow?.setAllowClose(true)
        client.stopReconnect()

        var params: [String: Any] = [:]
        if trayController?.forceRestoreProxyOnQuit == true {
            params["force_restore_proxy"] = true
        }
        if killAiClientsOnQuit {
            params["kill_ai_clients"] = true
        }

        var replied = false
        let finishTermination: () -> Void = { [weak self] in
            guard !replied else { return }
            replied = true
            self?.terminationCompleted = true
            NSApp.reply(toApplicationShouldTerminate: true)
        }

        client.call(method: "quit_daemon", params: params, timeoutSeconds: 2) { _, _, _, _ in
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.12) {
                finishTermination()
            }
        }
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.8) {
            finishTermination()
        }
    }

    func applicationWillTerminate(_ notification: Notification) {
        if let wakeObserver {
            NSWorkspace.shared.notificationCenter.removeObserver(wakeObserver)
            self.wakeObserver = nil
        }
        if let powerOffObserver {
            NSWorkspace.shared.notificationCenter.removeObserver(powerOffObserver)
            self.powerOffObserver = nil
        }
        stopControlSocket()
        if instanceLockFd >= 0 {
            Darwin.close(instanceLockFd)
            instanceLockFd = -1
        }
        daemonClient?.stopReconnect()
    }

    @objc private func handleGetURLEvent(_ event: NSAppleEventDescriptor, withReplyEvent replyEvent: NSAppleEventDescriptor) {
        guard let raw = event.paramDescriptor(forKeyword: AEKeyword(keyDirectObject))?.stringValue else {
            return
        }
        if trayController != nil {
            trayController?.importSchemeUri(raw)
        } else {
            startupUri = raw
        }
    }

    // MARK: - CLI & Smoke Mode

    private func parseArguments() {
        let args = CommandLine.arguments
        var i = 1
        while i < args.count {
            let a = args[i]
            if a == "--pipe", i + 1 < args.count {
                i += 1
                config.pipePath = args[i]
            } else if a == "--data-dir", i + 1 < args.count {
                i += 1
                config.dataDir = args[i]
            } else if a == "--bin-dir", i + 1 < args.count {
                i += 1
                config.binDir = args[i]
            } else if a == "--resource-dir", i + 1 < args.count {
                i += 1
                config.resourceDir = args[i]
            } else if a == "--dashboard-dir", i + 1 < args.count {
                i += 1
                config.dashboardDir = args[i]
            } else if a == "--smoke-test-ms", i + 1 < args.count {
                i += 1
                smokeTestMs = Int(args[i]) ?? 0
            } else if a == "--smoke-report", i + 1 < args.count {
                i += 1
                smokeReportPath = args[i]
            } else if a == "--background" || a == "--minimized" {
                backgroundStart = true
            } else if a.lowercased().hasPrefix("clash://") || a.lowercased().hasPrefix("clashfx://") {
                startupUri = a
            } else if [
                "--open-console", "--open-ai-lock", "--open-multi-node", "--open-rules",
                "--open-active-rules", "--open-subscriptions", "--open-diagnostics",
                "--open-settings", "--quit", "--quit-uninstall",
            ].contains(a) {
                startupCmd = a
            }
            i += 1
        }
    }

    private func scheduleSmokeTest() {
        let start = Date()
        let timeoutSec = Double(max(1500, smokeTestMs)) / 1000.0
        let pollTimer = Timer.scheduledTimer(withTimeInterval: 0.25, repeats: true) { [weak self] timer in
            guard let self, let console = self.consoleWindow, let client = self.daemonClient else {
                timer.invalidate()
                return
            }
            let elapsed = Date().timeIntervalSince(start)
            let snap = client.snapshot
            let coreRunning = (snap["core_running"] as? Bool) ?? false
            let allReady = console.isUiReady && client.isConnected && coreRunning
            if (allReady && elapsed >= 1.5) || elapsed >= timeoutSec {
                timer.invalidate()
                self.finishSmokeTest()
            }
        }
        RunLoop.main.add(pollTimer, forMode: .common)
    }

    private func finishSmokeTest() {
        guard let console = consoleWindow, let client = daemonClient else {
            exit(1)
        }
        let snap = client.snapshot
        let coreRunning = (snap["core_running"] as? Bool) ?? false
        let platform = (snap["platform"] as? String) ?? ""
        let basePassed = console.isUiReady && client.isConnected

        console.collectSmokeDomSummary { [weak self] dom in
            guard let self else { return }
            let domOk = (dom["ok"] as? Bool) ?? false
            let passed = basePassed && domOk
            let report: [String: Any] = [
                "ui_ready": console.isUiReady,
                "daemon_connected": client.isConnected,
                "core_running": coreRunning,
                "platform": platform,
                "dom": dom,
            ]
            if !self.smokeReportPath.isEmpty,
               let data = try? JSONSerialization.data(withJSONObject: report, options: [.prettyPrinted]) {
                try? data.write(to: URL(fileURLWithPath: self.smokeReportPath))
            }
            console.setAllowClose(true)
            client.stopReconnect()
            client.call(method: "quit_daemon", timeoutSeconds: 2) { _, _, _, _ in
                exit(passed ? 0 : 1)
            }
            DispatchQueue.main.asyncAfter(deadline: .now() + 1.0) {
                exit(passed ? 0 : 1)
            }
        }
    }

    private func installStandardMenus() {
        let mainMenu = NSMenu()
        NSApp.mainMenu = mainMenu

        let appItem = NSMenuItem()
        mainMenu.addItem(appItem)
        let appMenu = NSMenu()
        appItem.submenu = appMenu
        appMenu.addItem(withTitle: L("关闭窗口", "Close Window"), action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: L("隐藏 ClashFX", "Hide ClashFX"), action: #selector(NSApplication.hide(_:)), keyEquivalent: "h")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: L("退出 ClashFX", "Quit ClashFX"), action: #selector(quitFromMenu), keyEquivalent: "q")

        let editItem = NSMenuItem()
        mainMenu.addItem(editItem)
        let editMenu = NSMenu(title: "Edit")
        editItem.submenu = editMenu
        editMenu.addItem(withTitle: "Undo", action: Selector(("undo:")), keyEquivalent: "z")
        editMenu.addItem(withTitle: "Redo", action: Selector(("redo:")), keyEquivalent: "Z")
        editMenu.addItem(.separator())
        editMenu.addItem(withTitle: "Cut", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        editMenu.addItem(withTitle: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        editMenu.addItem(withTitle: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        editMenu.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
    }

    @objc private func quitFromMenu() {
        trayController?.quitAll(forceRestoreProxy: false)
    }

    // MARK: - Per-Pipe Single-Instance Control Socket

    private func pipeInstanceHash() -> String {
        var h: UInt64 = 14695981039346656037
        for b in config.pipePath.utf8 {
            h ^= UInt64(b)
            h &*= 1099511628211
        }
        return String(format: "%016llx", h)
    }

    private func acquireSingleInstanceOrForward() -> Bool {
        let runtimeDir = "/tmp/clashfx-\(getuid())"
        try? FileManager.default.createDirectory(
            atPath: runtimeDir,
            withIntermediateDirectories: true,
            attributes: [.posixPermissions: 0o700]
        )
        let hash = pipeInstanceHash()
        let lockPath = (runtimeDir as NSString).appendingPathComponent("ui-\(hash).lock")
        let sockPath = (runtimeDir as NSString).appendingPathComponent("ui-\(hash).sock")

        let fd = Darwin.open(lockPath, O_CREAT | O_RDWR, 0o600)
        if fd < 0 {
            return true
        }
        _ = Darwin.fcntl(fd, F_SETFD, FD_CLOEXEC)

        var fl = Darwin.flock()
        fl.l_type = Int16(F_WRLCK)
        fl.l_whence = Int16(SEEK_SET)
        fl.l_start = 0
        fl.l_len = 0
        if Darwin.fcntl(fd, F_SETLK, &fl) != 0 {
            let payload: String
            if !startupUri.isEmpty {
                payload = "URI:\(startupUri)"
            } else if !startupCmd.isEmpty {
                payload = startupCmd
            } else {
                payload = "ACTIVATE"
            }
            forwardCommandToRunningInstance(sockPath: sockPath, command: payload)
            Darwin.close(fd)
            return false
        }

        instanceLockFd = fd
        controlSocketPath = sockPath
        startControlSocketListener(sockPath: sockPath)
        return true
    }

    private func forwardCommandToRunningInstance(sockPath: String, command: String) {
        let utf8 = Array(sockPath.utf8)
        guard utf8.count < 104 else { return }
        let fd = Darwin.socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { return }
        defer { Darwin.close(fd) }

        var addr = sockaddr_un()
        addr.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
        addr.sun_family = sa_family_t(AF_UNIX)
        withUnsafeMutableBytes(of: &addr.sun_path) { rawBuf in
            rawBuf.initializeMemory(as: UInt8.self, repeating: 0)
            for (idx, b) in utf8.enumerated() where idx + 1 < rawBuf.count {
                rawBuf[idx] = b
            }
        }
        let connRes = withUnsafePointer(to: &addr) { ptr -> Int32 in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockPtr in
                Darwin.connect(fd, sockPtr, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        guard connRes == 0 else { return }
        let lineBytes = Array((command + "\n").utf8)
        _ = lineBytes.withUnsafeBytes { buf in
            guard let base = buf.baseAddress else { return 0 }
            return Darwin.write(fd, base, buf.count)
        }
    }

    private func startControlSocketListener(sockPath: String) {
        let utf8 = Array(sockPath.utf8)
        guard utf8.count < 104 else { return }
        Darwin.unlink(sockPath)
        let fd = Darwin.socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { return }
        _ = Darwin.fcntl(fd, F_SETFD, FD_CLOEXEC)

        var addr = sockaddr_un()
        addr.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
        addr.sun_family = sa_family_t(AF_UNIX)
        withUnsafeMutableBytes(of: &addr.sun_path) { rawBuf in
            rawBuf.initializeMemory(as: UInt8.self, repeating: 0)
            for (idx, b) in utf8.enumerated() where idx + 1 < rawBuf.count {
                rawBuf[idx] = b
            }
        }
        let bindRes = withUnsafePointer(to: &addr) { ptr -> Int32 in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockPtr in
                Darwin.bind(fd, sockPtr, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        guard bindRes == 0, Darwin.listen(fd, 16) == 0 else {
            Darwin.close(fd)
            return
        }
        _ = Darwin.chmod(sockPath, 0o600)
        controlListenFd = fd

        DispatchQueue.global(qos: .utility).async { [weak self] in
            var buf = [UInt8](repeating: 0, count: 4096)
            while true {
                let connFd = Darwin.accept(fd, nil, nil)
                if connFd < 0 {
                    if errno == EINTR { continue }
                    break
                }
                var collected = Data()
                while collected.count < 16384 {
                    let n = Darwin.read(connFd, &buf, buf.count)
                    if n <= 0 { break }
                    collected.append(contentsOf: buf[0..<n])
                    if collected.contains(0x0A) { break }
                }
                Darwin.close(connFd)
                if let msg = String(data: collected, encoding: .utf8)?
                    .trimmingCharacters(in: .whitespacesAndNewlines),
                   !msg.isEmpty {
                    DispatchQueue.main.async { [weak self] in
                        self?.trayController?.handleExternalCommand(msg)
                    }
                }
            }
        }
    }

    private func stopControlSocket() {
        if controlListenFd >= 0 {
            Darwin.close(controlListenFd)
            controlListenFd = -1
        }
        if !controlSocketPath.isEmpty {
            Darwin.unlink(controlSocketPath)
            controlSocketPath = ""
        }
    }
}
