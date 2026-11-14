import Cocoa
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
        installStandardMenus()

        let client = DaemonClient(config: config)
        let showInitially = !backgroundStart || smokeTestMs > 0
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

    func applicationWillTerminate(_ notification: Notification) {
        if let wakeObserver {
            NSWorkspace.shared.notificationCenter.removeObserver(wakeObserver)
            self.wakeObserver = nil
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
        let passed = console.isUiReady && client.isConnected

        console.collectSmokeDomSummary { [weak self] dom in
            guard let self else { return }
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
}
