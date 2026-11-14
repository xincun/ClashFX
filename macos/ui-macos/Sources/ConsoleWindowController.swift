import Cocoa
import Darwin
import WebKit

private final class WeakScriptHandler: NSObject, WKScriptMessageHandler {
    weak var target: WKScriptMessageHandler?

    init(target: WKScriptMessageHandler) {
        self.target = target
    }

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        target?.userContentController(userContentController, didReceive: message)
    }
}

private final class LoopbackHTTPServer {
    private(set) var port: UInt16 = 0

    private let webRootURL: URL
    private let acceptQueue = DispatchQueue(label: "com.clashfx.http.accept")
    private let workerQueue = DispatchQueue(label: "com.clashfx.http.worker", attributes: .concurrent)
    private let lock = NSLock()
    private var listenFd: Int32 = -1
    private var running = false

    init(webRootURL: URL) {
        self.webRootURL = webRootURL.standardizedFileURL
    }

    deinit {
        stop()
    }

    func start() -> UInt16 {
        lock.lock()
        defer { lock.unlock() }
        if running && port > 0 {
            return port
        }

        let fd = Darwin.socket(AF_INET, SOCK_STREAM, 0)
        guard fd >= 0 else { return 0 }

        var yes: Int32 = 1
        _ = Darwin.setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &yes, socklen_t(MemoryLayout<Int32>.size))
        _ = Darwin.setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &yes, socklen_t(MemoryLayout<Int32>.size))

        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = 0
        addr.sin_addr = in_addr(s_addr: inet_addr("127.0.0.1"))

        let bindRes = withUnsafePointer(to: &addr) { ptr -> Int32 in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockPtr in
                Darwin.bind(fd, sockPtr, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        guard bindRes == 0 else {
            Darwin.close(fd)
            return 0
        }

        var boundAddr = sockaddr_in()
        var addrLen = socklen_t(MemoryLayout<sockaddr_in>.size)
        let nameRes = withUnsafeMutablePointer(to: &boundAddr) { ptr -> Int32 in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockPtr in
                Darwin.getsockname(fd, sockPtr, &addrLen)
            }
        }
        guard nameRes == 0, Darwin.listen(fd, 64) == 0 else {
            Darwin.close(fd)
            return 0
        }

        listenFd = fd
        port = UInt16(bigEndian: boundAddr.sin_port)
        running = true

        acceptQueue.async { [weak self] in
            self?.acceptLoop(listenFd: fd)
        }
        return port
    }

    func stop() {
        lock.lock()
        running = false
        let fd = listenFd
        listenFd = -1
        lock.unlock()

        if fd >= 0 {
            Darwin.shutdown(fd, SHUT_RDWR)
            Darwin.close(fd)
        }
    }

    private func acceptLoop(listenFd: Int32) {
        while true {
            var clientAddr = sockaddr_in()
            var clientLen = socklen_t(MemoryLayout<sockaddr_in>.size)
            let clientFd = withUnsafeMutablePointer(to: &clientAddr) { ptr -> Int32 in
                ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockPtr in
                    Darwin.accept(listenFd, sockPtr, &clientLen)
                }
            }
            if clientFd < 0 {
                if errno == EINTR { continue }
                return
            }
            var noSigPipe: Int32 = 1
            _ = Darwin.setsockopt(clientFd, SOL_SOCKET, SO_NOSIGPIPE, &noSigPipe, socklen_t(MemoryLayout<Int32>.size))
            workerQueue.async { [weak self] in
                self?.handleClient(fd: clientFd)
            }
        }
    }

    private func handleClient(fd: Int32) {
        defer { Darwin.close(fd) }

        var tv = timeval(tv_sec: 5, tv_usec: 0)
        _ = Darwin.setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))

        var reqData = Data()
        var buf = [UInt8](repeating: 0, count: 4096)
        let headerEnd = Data([0x0D, 0x0A, 0x0D, 0x0A])
        while reqData.count < 32768 {
            let n = Darwin.read(fd, &buf, buf.count)
            if n <= 0 { break }
            reqData.append(contentsOf: buf[0..<n])
            if reqData.range(of: headerEnd) != nil { break }
        }

        guard let reqText = String(data: reqData, encoding: .utf8),
              let firstLine = reqText.components(separatedBy: "\r\n").first else {
            return
        }
        let parts = firstLine.split(separator: " ")
        guard parts.count >= 2 else {
            sendResponse(fd: fd, status: "400 Bad Request", contentType: "text/plain", body: Data("Bad Request".utf8))
            return
        }
        let method = String(parts[0]).uppercased()
        guard method == "GET" || method == "HEAD" else {
            sendResponse(fd: fd, status: "405 Method Not Allowed", contentType: "text/plain", body: Data("Method Not Allowed".utf8))
            return
        }

        var rawPath = String(parts[1])
        if let qIdx = rawPath.firstIndex(of: "?") {
            rawPath = String(rawPath[..<qIdx])
        }
        let decoded = rawPath.removingPercentEncoding ?? rawPath
        if decoded == "/" || decoded.isEmpty {
            rawPath = "/index.html"
        } else {
            rawPath = decoded
        }

        let trimmed = rawPath.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        if trimmed.split(separator: "/").contains("..") {
            sendResponse(fd: fd, status: "403 Forbidden", contentType: "text/plain", body: Data("Forbidden".utf8))
            return
        }

        let candidate = webRootURL.appendingPathComponent(trimmed).standardizedFileURL
        let rootPrefix = webRootURL.path.hasSuffix("/") ? webRootURL.path : (webRootURL.path + "/")
        guard candidate.path == webRootURL.path || candidate.path.hasPrefix(rootPrefix) else {
            sendResponse(fd: fd, status: "403 Forbidden", contentType: "text/plain", body: Data("Forbidden".utf8))
            return
        }

        var finalURL = candidate
        var isDir: ObjCBool = false
        if FileManager.default.fileExists(atPath: finalURL.path, isDirectory: &isDir), isDir.boolValue {
            finalURL = finalURL.appendingPathComponent("index.html")
        }

        guard let fileData = try? Data(contentsOf: finalURL) else {
            sendResponse(fd: fd, status: "404 Not Found", contentType: "text/plain", body: Data("Not Found".utf8))
            return
        }

        let ctype = mimeType(for: finalURL.pathExtension.lowercased())
        sendResponse(
            fd: fd,
            status: "200 OK",
            contentType: ctype,
            body: method == "HEAD" ? Data() : fileData,
            explicitContentLength: fileData.count
        )
    }

    private func mimeType(for ext: String) -> String {
        switch ext {
        case "html", "htm": return "text/html; charset=utf-8"
        case "js", "mjs": return "application/javascript; charset=utf-8"
        case "css": return "text/css; charset=utf-8"
        case "svg": return "image/svg+xml"
        case "png": return "image/png"
        case "jpg", "jpeg": return "image/jpeg"
        case "ico": return "image/x-icon"
        case "json": return "application/json; charset=utf-8"
        default: return "application/octet-stream"
        }
    }

    private func sendResponse(
        fd: Int32,
        status: String,
        contentType: String,
        body: Data,
        explicitContentLength: Int? = nil
    ) {
        let len = explicitContentLength ?? body.count
        let header = "HTTP/1.1 \(status)\r\nContent-Type: \(contentType)\r\nContent-Length: \(len)\r\nCache-Control: no-store\r\nConnection: close\r\n\r\n"
        var payload = Data(header.utf8)
        payload.append(body)
        payload.withUnsafeBytes { rawBuf in
            guard let base = rawBuf.baseAddress else { return }
            var sent = 0
            while sent < payload.count {
                let n = Darwin.write(fd, base.advanced(by: sent), payload.count - sent)
                if n <= 0 {
                    if errno == EINTR { continue }
                    return
                }
                sent += n
            }
        }
    }
}

final class ConsoleWindowController: NSObject, NSWindowDelegate, WKScriptMessageHandler, WKNavigationDelegate, WKUIDelegate, URLSessionDataDelegate {
    private let client: DaemonClient
    private let httpServer: LoopbackHTTPServer
    private let window: NSWindow
    private let webView: WKWebView

    private var theme: String
    private var uiReady = false
    private var connected = false
    private var snapshot: [String: Any] = [:]
    private var currentPage = 0
    private var pendingSubTab = -1
    private var pendingDiagNode = ""
    private var allowClose = false
    private var i18nObserverID: UUID?

    private var telemetryConnections = false
    private var telemetryTimer: Timer?
    private var trafficSession: URLSession?
    private var trafficTask: URLSessionDataTask?
    private var trafficBuffer = Data()
    private var activeTrafficPort = 0
    private var activeTrafficSecret = ""
    private var lastUpRate: Int64 = 0
    private var lastDownRate: Int64 = 0

    var isUiReady: Bool { uiReady }
    var isVisible: Bool { window.isVisible }

    init(client: DaemonClient) {
        self.client = client
        let webRoot = URL(fileURLWithPath: client.config.resourceDir).appendingPathComponent("web")
        self.httpServer = LoopbackHTTPServer(webRootURL: webRoot)
        let serverPort = self.httpServer.start()

        let savedTheme = UserDefaults.standard.string(forKey: "ui.theme") ?? "dark"
        self.theme = (savedTheme == "light") ? "light" : "dark"

        let rect = NSRect(x: 0, y: 0, width: 1220, height: 780)
        self.window = NSWindow(
            contentRect: rect,
            styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView],
            backing: .buffered,
            defer: false
        )
        window.title = "ClashFX"
        window.titleVisibility = .hidden
        window.titlebarAppearsTransparent = true
        window.isMovableByWindowBackground = false
        window.minSize = NSSize(width: 960, height: 620)
        window.isReleasedWhenClosed = false
        window.center()

        let config = WKWebViewConfiguration()
        config.preferences.javaScriptCanOpenWindowsAutomatically = false
        let controller = WKUserContentController()
        config.userContentController = controller
        self.webView = WKWebView(frame: rect, configuration: config)
        self.webView.setValue(false, forKey: "drawsBackground")

        super.init()

        window.delegate = self
        webView.navigationDelegate = self
        webView.uiDelegate = self
        controller.add(WeakScriptHandler(target: self), name: "clashfx")
        installSubframeScripts(controller: controller, serverPort: serverPort)

        window.contentView = webView
        applyNativeThemeAppearance()

        i18nObserverID = I18n.shared.addObserver { [weak self] lang in
            guard let self, self.uiReady else { return }
            self.postJsonMessage(["type": "language", "language": lang])
        }

        if serverPort > 0, let url = URL(string: "http://127.0.0.1:\(serverPort)/index.html") {
            webView.load(URLRequest(url: url))
        }
    }

    deinit {
        telemetryTimer?.invalidate()
        stopTrafficStream()
        httpServer.stop()
        if let id = i18nObserverID {
            _ = id
        }
    }

    func setAllowClose(_ allow: Bool) {
        allowClose = allow
    }

    func navigateTo(page: Int, subTab: Int = -1, diagNode: String = "") {
        currentPage = max(0, min(6, page))
        pendingSubTab = subTab
        pendingDiagNode = diagNode

        guard uiReady else { return }
        var msg: [String: Any] = [
            "type": "navigate",
            "page": currentPage,
            "subTab": pendingSubTab,
        ]
        if !pendingDiagNode.isEmpty {
            msg["diagNode"] = pendingDiagNode
        }
        postJsonMessage(msg)
    }

    func presentWindow(activateApp: Bool = true) {
        if activateApp {
            NSApp.setActivationPolicy(.regular)
        }
        if window.isMiniaturized {
            window.deminiaturize(nil)
        }
        window.makeKeyAndOrderFront(nil)
        if activateApp {
            NSApp.activate(ignoringOtherApps: true)
        }
        pushWindowState()
        syncTelemetryState()
    }

    func applySnapshot(connected: Bool, snapshot: [String: Any]) {
        self.connected = connected
        self.snapshot = snapshot

        if uiReady {
            postJsonMessage([
                "type": "state",
                "connected": connected,
                "snapshot": snapshot,
            ])
        }
        syncTelemetryState()
    }

    func collectSmokeDomSummary(completion: @escaping ([String: Any]) -> Void) {
        let js = """
        (function() {
          var navBtns = document.querySelectorAll('.nav-item');
          var activeBtn = document.querySelector('.nav-item.active');
          var chip = document.getElementById('topbar-chip');
          var frame = document.getElementById('metacubexd-frame');
          var standby = document.getElementById('console-standby');
          return {
            platformAttr: document.documentElement ? document.documentElement.getAttribute('data-platform') : '',
            navCount: navBtns ? navBtns.length : 0,
            activeNavText: activeBtn ? String(activeBtn.textContent || '').trim() : '',
            chipText: chip ? String(chip.textContent || '').trim() : '',
            iframeSrc: frame ? String(frame.getAttribute('src') || frame.src || '') : '',
            standbyHidden: standby ? Boolean(standby.hidden) : false,
            bodyLen: document.body ? document.body.innerHTML.length : 0
          };
        })();
        """
        webView.evaluateJavaScript(js) { value, err in
            if let dict = value as? [String: Any] {
                completion(dict)
            } else {
                completion(["evalError": err?.localizedDescription ?? "unknown"])
            }
        }
    }

    // MARK: - NSWindowDelegate

    func windowShouldClose(_ sender: NSWindow) -> Bool {
        if allowClose {
            return true
        }
        sender.orderOut(nil)
        telemetryConnections = false
        syncTelemetryState()
        if !NSApp.windows.contains(where: { $0 !== sender && $0.isVisible }) {
            NSApp.setActivationPolicy(.accessory)
        }
        return false
    }

    func windowDidResize(_ notification: Notification) {
        pushWindowState()
    }

    func windowDidEnterFullScreen(_ notification: Notification) {
        pushWindowState()
    }

    func windowDidExitFullScreen(_ notification: Notification) {
        pushWindowState()
    }

    // MARK: - WKUIDelegate & WKNavigationDelegate

    func webView(
        _ webView: WKWebView,
        decidePolicyFor navigationAction: WKNavigationAction,
        decisionHandler: @escaping (WKNavigationActionPolicy) -> Void
    ) {
        guard let url = navigationAction.request.url else {
            decisionHandler(.allow)
            return
        }
        let host = url.host?.lowercased() ?? ""
        if url.scheme == "about" || host == "127.0.0.1" || host == "localhost" {
            decisionHandler(.allow)
            return
        }
        if navigationAction.targetFrame?.isMainFrame == true,
           (url.scheme == "http" || url.scheme == "https") {
            NSWorkspace.shared.open(url)
            decisionHandler(.cancel)
            return
        }
        decisionHandler(.allow)
    }

    func webView(
        _ webView: WKWebView,
        runJavaScriptAlertPanelWithMessage message: String,
        initiatedByFrame frame: WKFrameInfo,
        completionHandler: @escaping () -> Void
    ) {
        let alert = NSAlert()
        alert.messageText = "ClashFX"
        alert.informativeText = message
        alert.addButton(withTitle: L("确定", "OK"))
        if window.isVisible {
            alert.beginSheetModal(for: window) { _ in completionHandler() }
        } else {
            alert.runModal()
            completionHandler()
        }
    }

    func webView(
        _ webView: WKWebView,
        runJavaScriptConfirmPanelWithMessage message: String,
        initiatedByFrame frame: WKFrameInfo,
        completionHandler: @escaping (Bool) -> Void
    ) {
        let alert = NSAlert()
        alert.messageText = "ClashFX"
        alert.informativeText = message
        alert.addButton(withTitle: L("确定", "Confirm"))
        alert.addButton(withTitle: L("取消", "Cancel"))
        if window.isVisible {
            alert.beginSheetModal(for: window) { resp in
                completionHandler(resp == .alertFirstButtonReturn)
            }
        } else {
            completionHandler(alert.runModal() == .alertFirstButtonReturn)
        }
    }

    func webView(
        _ webView: WKWebView,
        runJavaScriptTextInputPanelWithPrompt prompt: String,
        defaultText: String?,
        initiatedByFrame frame: WKFrameInfo,
        completionHandler: @escaping (String?) -> Void
    ) {
        let alert = NSAlert()
        alert.messageText = "ClashFX"
        alert.informativeText = prompt
        alert.addButton(withTitle: L("确定", "OK"))
        alert.addButton(withTitle: L("取消", "Cancel"))
        let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 280, height: 24))
        field.stringValue = defaultText ?? ""
        alert.accessoryView = field
        if window.isVisible {
            alert.beginSheetModal(for: window) { resp in
                completionHandler(resp == .alertFirstButtonReturn ? field.stringValue : nil)
            }
        } else {
            let resp = alert.runModal()
            completionHandler(resp == .alertFirstButtonReturn ? field.stringValue : nil)
        }
    }

    // MARK: - WKScriptMessageHandler

    func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
        guard let msg = message.body as? [String: Any],
              let type = msg["type"] as? String else {
            return
        }

        switch type {
        case "ui_ready":
            uiReady = true
            pushInitPayload()
            syncTelemetryState()
        case "nav_sync":
            if let pageIdx = msg["page"] as? Int, pageIdx >= 0 && pageIdx <= 6 {
                currentPage = pageIdx
            }
            pendingSubTab = (msg["subTab"] as? Int) ?? -1
            pendingDiagNode = ""
        case "rpc":
            handleRpcMessage(msg)
        case "mihomo_api":
            handleMihomoMessage(msg)
        case "native_action":
            handleNativeAction(msg)
        case "set_theme":
            applyThemePreference((msg["theme"] as? String) ?? "dark")
        case "set_language":
            let lang = (msg["language"] as? String) ?? "zh_CN"
            if lang == "zh_CN" || lang == "en_US" {
                I18n.shared.setLanguage(lang)
                persistSetting(key: "language", value: lang)
            }
        case "telemetry_subscribe":
            let enabled = (msg["enabled"] as? Bool) ?? false
            let includeConn = (msg["include_connections"] as? Bool) ?? false
            telemetryConnections = enabled && includeConn
            syncTelemetryState()
        case "window_drag":
            if let ev = NSApp.currentEvent,
               ev.type == .leftMouseDown || ev.type == .leftMouseDragged {
                window.performDrag(with: ev)
            }
        case "window_control":
            let action = (msg["action"] as? String) ?? ""
            if action == "minimize" {
                window.miniaturize(nil)
            } else if action == "toggle_maximize" {
                window.zoom(nil)
            } else if action == "close" {
                window.performClose(nil)
            }
        default:
            break
        }
    }

    // MARK: - Bridge Helpers

    private func postJsonMessage(_ payload: [String: Any]) {
        guard let data = try? JSONSerialization.data(withJSONObject: payload, options: []),
              let jsonStr = String(data: data, encoding: .utf8) else {
            return
        }
        let js = "window.__clashfxReceive && window.__clashfxReceive(\(jsonStr));"
        webView.evaluateJavaScript(js, completionHandler: nil)
    }

    private func pushWindowState() {
        guard uiReady else { return }
        postJsonMessage([
            "type": "window_state",
            "maximized": window.isZoomed,
            "fullscreen": window.styleMask.contains(.fullScreen),
        ])
    }

    private func pushInitPayload() {
        var initMsg: [String: Any] = [
            "type": "init",
            "connected": connected,
            "platform": "darwin",
            "snapshot": snapshot,
            "language": I18n.shared.language,
            "theme": theme,
            "page": currentPage,
            "subTab": pendingSubTab,
            "maximized": window.isZoomed,
            "fullscreen": window.styleMask.contains(.fullScreen),
        ]
        if !pendingDiagNode.isEmpty {
            initMsg["diagNode"] = pendingDiagNode
        }
        postJsonMessage(initMsg)
    }

    private func applyThemePreference(_ rawTheme: String) {
        let next = (rawTheme == "light") ? "light" : "dark"
        guard theme != next else { return }
        theme = next
        UserDefaults.standard.set(next, forKey: "ui.theme")
        applyNativeThemeAppearance()
    }

    private func applyNativeThemeAppearance() {
        let isDark = (theme == "dark")
        window.appearance = NSAppearance(named: isDark ? .darkAqua : .aqua)
        window.backgroundColor = isDark
            ? NSColor(calibratedRed: 9.0 / 255.0, green: 13.0 / 255.0, blue: 22.0 / 255.0, alpha: 1.0)
            : NSColor(calibratedRed: 245.0 / 255.0, green: 247.0 / 255.0, blue: 251.0 / 255.0, alpha: 1.0)
    }

    private func persistSetting(key: String, value: Any) {
        guard var settings = snapshot["settings"] as? [String: Any] else { return }
        settings[key] = value
        client.call(method: "update_settings", params: ["settings": settings])
    }

    private func handleRpcMessage(_ msg: [String: Any]) {
        let id = msg["id"] ?? 0
        let method = (msg["method"] as? String) ?? ""
        let params = (msg["params"] as? [String: Any]) ?? [:]
        if method.isEmpty {
            postJsonMessage([
                "type": "rpc_result", "id": id, "ok": false,
                "error": "RPC method is required", "error_code": "",
            ])
            return
        }

        let longMethods: Set<String> = [
            "benchmark_all", "benchmark_group", "refresh_subscription",
            "refresh_all_subscriptions", "add_subscription", "update_geo_databases",
            "run_node_diagnostics", "test_node_download_speeds", "install_service",
        ]
        let timeoutSec = longMethods.contains(method) ? 60 : 25
        client.call(method: method, params: params, timeoutSeconds: timeoutSec) { [weak self] ok, data, err, errCode in
            self?.postJsonMessage([
                "type": "rpc_result",
                "id": id,
                "ok": ok,
                "data": data ?? NSNull(),
                "error": I18n.translateBackendText(err),
                "error_code": errCode,
            ])
        }
    }

    private func handleMihomoMessage(_ msg: [String: Any]) {
        let id = msg["id"] ?? 0
        let runtime = (snapshot["runtime"] as? [String: Any]) ?? [:]
        let apiPort = (runtime["api_port"] as? Int) ?? 0
        let secret = (runtime["api_secret"] as? String) ?? ""
        if apiPort <= 0 {
            postJsonMessage([
                "type": "mihomo_result", "id": id, "ok": false, "status": 0,
                "error": "Mihomo API port unavailable",
            ])
            return
        }

        var path = (msg["path"] as? String) ?? ""
        if !path.hasPrefix("/") {
            path = "/" + path
        }
        guard let url = URL(string: "http://127.0.0.1:\(apiPort)\(path)") else {
            postJsonMessage([
                "type": "mihomo_result", "id": id, "ok": false, "status": 0,
                "error": "Invalid Mihomo path",
            ])
            return
        }

        let method = ((msg["method"] as? String) ?? "GET").uppercased()
        var request = URLRequest(url: url, timeoutInterval: 15)
        request.httpMethod = method
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if !secret.isEmpty {
            request.setValue("Bearer \(secret)", forHTTPHeaderField: "Authorization")
        }
        if let bodyVal = msg["body"], !(bodyVal is NSNull), method != "GET", method != "DELETE" {
            if let str = bodyVal as? String {
                request.httpBody = str.data(using: .utf8)
            } else if let objData = try? JSONSerialization.data(withJSONObject: bodyVal, options: []) {
                request.httpBody = objData
            }
        }

        URLSession.shared.dataTask(with: request) { [weak self] data, response, error in
            let status = (response as? HTTPURLResponse)?.statusCode ?? 0
            let ok = (error == nil && status >= 200 && status < 300)
            var parsedData: Any = NSNull()
            if let data, !data.isEmpty {
                if let jsonObj = try? JSONSerialization.jsonObject(with: data, options: [.fragmentsAllowed]) {
                    parsedData = jsonObj
                } else if let str = String(data: data, encoding: .utf8) {
                    parsedData = str
                }
            }
            DispatchQueue.main.async {
                self?.postJsonMessage([
                    "type": "mihomo_result",
                    "id": id,
                    "ok": ok,
                    "status": status,
                    "data": parsedData,
                    "error": error?.localizedDescription ?? "",
                ])
            }
        }.resume()
    }

    private func handleNativeAction(_ msg: [String: Any]) {
        let id = msg["id"] ?? 0
        let action = (msg["action"] as? String) ?? ""

        let replyOk: ([String: Any]) -> Void = { [weak self] data in
            self?.postJsonMessage([
                "type": "native_result", "id": id, "ok": true, "data": data,
            ])
        }
        let replyErr: (String) -> Void = { [weak self] err in
            self?.postJsonMessage([
                "type": "native_result", "id": id, "ok": false, "error": err,
            ])
        }

        switch action {
        case "copy_text":
            let pb = NSPasteboard.general
            pb.clearContents()
            pb.setString((msg["text"] as? String) ?? "", forType: .string)
            replyOk(["copied": true])

        case "read_clipboard":
            let text = NSPasteboard.general.string(forType: .string) ?? ""
            replyOk(["text": text])

        case "open_path":
            let path = (msg["path"] as? String) ?? ""
            guard !path.isEmpty else {
                replyErr("Empty path")
                return
            }
            NSWorkspace.shared.open(URL(fileURLWithPath: path))
            replyOk([:])

        case "open_url":
            let raw = ((msg["url"] as? String) ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            guard let url = URL(string: raw),
                  let scheme = url.scheme?.lowercased(),
                  scheme == "http" || scheme == "https" else {
                replyErr("Invalid URL")
                return
            }
            NSWorkspace.shared.open(url)
            replyOk([:])

        case "pick_directory":
            let panel = NSOpenPanel()
            panel.canChooseFiles = false
            panel.canChooseDirectories = true
            panel.allowsMultipleSelection = false
            panel.title = (msg["title"] as? String) ?? L("选择文件夹", "Select Directory")
            if let initial = msg["initial"] as? String, !initial.isEmpty {
                panel.directoryURL = URL(fileURLWithPath: initial)
            }
            let finishPick: (NSApplication.ModalResponse) -> Void = { resp in
                let chosen = (resp == .OK) ? (panel.url?.path ?? "") : ""
                replyOk(["path": chosen])
            }
            if window.isVisible {
                panel.beginSheetModal(for: window, completionHandler: finishPick)
            } else {
                finishPick(panel.runModal())
            }

        case "launch_chrome_proxy":
            let port = (msg["port"] as? Int) ?? 0
            guard port > 0 && port <= 65535 else {
                replyErr("Invalid proxy port")
                return
            }
            let candidates = [
                "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
                "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
                "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
                "/Applications/Chromium.app/Contents/MacOS/Chromium",
            ]
            guard let exe = candidates.first(where: { FileManager.default.isExecutableFile(atPath: $0) }) else {
                replyErr(L("未找到 Google Chrome / Edge / Chromium 浏览器。", "Google Chrome / Edge / Chromium not found in /Applications."))
                return
            }
            let profileDir = (NSTemporaryDirectory() as NSString).appendingPathComponent("clashfx-chrome-profile")
            try? FileManager.default.createDirectory(atPath: profileDir, withIntermediateDirectories: true)
            let proc = Process()
            proc.executableURL = URL(fileURLWithPath: exe)
            proc.arguments = [
                "--proxy-server=http://127.0.0.1:\(port)",
                "--user-data-dir=\(profileDir)",
                "--no-first-run",
                "--no-default-browser-check",
            ]
            proc.standardInput = FileHandle.nullDevice
            proc.standardOutput = FileHandle.nullDevice
            proc.standardError = FileHandle.nullDevice
            do {
                try proc.run()
                replyOk(["exe": exe])
            } catch {
                replyErr(error.localizedDescription)
            }

        case "reload_ui":
            webView.reload()
            replyOk([:])

        default:
            replyErr("Unknown native action")
        }
    }

    // MARK: - Telemetry (/traffic NDJSON + /connections 1.5s poll)

    private func syncTelemetryState() {
        let runtime = (snapshot["runtime"] as? [String: Any]) ?? [:]
        let coreRunning = (snapshot["core_running"] as? Bool) ?? false
        let apiPort = (runtime["api_port"] as? Int) ?? 0
        let active = window.isVisible && uiReady && connected && coreRunning && apiPort > 0

        if !active {
            stopTrafficStream()
            telemetryTimer?.invalidate()
            telemetryTimer = nil
            lastUpRate = 0
            lastDownRate = 0
            return
        }

        startTrafficStream()
        if telemetryConnections {
            if telemetryTimer == nil {
                let timer = Timer.scheduledTimer(withTimeInterval: 1.5, repeats: true) { [weak self] _ in
                    self?.pollTelemetrySnapshot()
                }
                RunLoop.main.add(timer, forMode: .common)
                telemetryTimer = timer
                pollTelemetrySnapshot()
            }
        } else {
            telemetryTimer?.invalidate()
            telemetryTimer = nil
        }
    }

    private func startTrafficStream() {
        let runtime = (snapshot["runtime"] as? [String: Any]) ?? [:]
        let apiPort = (runtime["api_port"] as? Int) ?? 0
        let secret = (runtime["api_secret"] as? String) ?? ""
        guard apiPort > 0 else { return }

        if trafficTask != nil && activeTrafficPort == apiPort && activeTrafficSecret == secret {
            return
        }
        stopTrafficStream()

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

    private func stopTrafficStream() {
        trafficTask?.cancel()
        trafficTask = nil
        trafficSession?.invalidateAndCancel()
        trafficSession = nil
        trafficBuffer.removeAll(keepingCapacity: false)
        activeTrafficPort = 0
        activeTrafficSecret = ""
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        guard session === trafficSession else { return }
        trafficBuffer.append(data)
        while let nlRange = trafficBuffer.firstRange(of: Data([0x0A])) {
            let line = trafficBuffer.subdata(in: 0..<nlRange.lowerBound)
            trafficBuffer.removeSubrange(0..<nlRange.upperBound)
            guard !line.isEmpty,
                  let obj = try? JSONSerialization.jsonObject(with: line, options: []) as? [String: Any] else {
                continue
            }
            let up = (obj["up"] as? NSNumber)?.int64Value ?? 0
            let down = (obj["down"] as? NSNumber)?.int64Value ?? 0
            lastUpRate = up
            lastDownRate = down
            postJsonMessage([
                "type": "telemetry",
                "up": up,
                "down": down,
            ])
        }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        guard session === trafficSession else { return }
        trafficTask = nil
    }

    private func pollTelemetrySnapshot() {
        let runtime = (snapshot["runtime"] as? [String: Any]) ?? [:]
        let apiPort = (runtime["api_port"] as? Int) ?? 0
        let secret = (runtime["api_secret"] as? String) ?? ""
        guard apiPort > 0, let url = URL(string: "http://127.0.0.1:\(apiPort)/connections") else { return }

        var req = URLRequest(url: url, timeoutInterval: 2.0)
        if !secret.isEmpty {
            req.setValue("Bearer \(secret)", forHTTPHeaderField: "Authorization")
        }
        URLSession.shared.dataTask(with: req) { [weak self] data, _, error in
            guard error == nil, let data,
                  let obj = try? JSONSerialization.jsonObject(with: data, options: []) as? [String: Any] else {
                return
            }
            let upTotal = (obj["uploadTotal"] as? NSNumber)?.int64Value ?? 0
            let downTotal = (obj["downloadTotal"] as? NSNumber)?.int64Value ?? 0
            let memory = (obj["memory"] as? NSNumber)?.int64Value ?? 0
            let conns = (obj["connections"] as? [[String: Any]]) ?? []
            DispatchQueue.main.async {
                guard let self, self.telemetryConnections else { return }
                self.postJsonMessage([
                    "type": "telemetry",
                    "up": self.lastUpRate,
                    "down": self.lastDownRate,
                    "upTotal": upTotal,
                    "downTotal": downTotal,
                    "memory": memory,
                    "connectionCount": conns.count,
                    "connections": Array(conns.prefix(250)),
                ])
            }
        }.resume()
    }

    // MARK: - Subframe Script Injection (MetaCubeXD Compat + Guards)

    private func readBundledCompatJS() -> String {
        let resDir = client.config.resourceDir
        let candidates = [
            (resDir as NSString).appendingPathComponent("DashboardCompatibility/clashfx-compat.js"),
            (resDir as NSString).appendingPathComponent("clashfx-compat.js"),
        ]
        for path in candidates {
            if let text = try? String(contentsOfFile: path, encoding: .utf8), !text.isEmpty {
                return text
            }
        }
        return ""
    }

    private func installSubframeScripts(controller: WKUserContentController, serverPort: UInt16) {
        let parentOrigin = "http://127.0.0.1:\(serverPort)"
        let compatJs = readBundledCompatJS()
        let script = """
        (function() {
          if (window === window.top) return;
          if (location.hostname !== '127.0.0.1' && location.hostname !== 'localhost') return;

          var cfxSecret = '';
          var cfxTheme = '';
          var cfxLang = '';
          var cfxAi = false;
          try {
            var h = String(location.hash || '');
            var qPos = h.indexOf('?');
            if (qPos >= 0) {
              var sp = new URLSearchParams(h.slice(qPos + 1));
              cfxSecret = sp.get('secret') || '';
              cfxTheme = sp.get('cfx_theme') || '';
              cfxLang = sp.get('cfx_lang') || '';
              cfxAi = sp.get('cfx_ai') === '1';
            }
            window.__CLASHFX_AI_LOCKED__ = cfxAi;
            window.__METACUBEXD_CONFIG__ = Object.assign({}, window.__METACUBEXD_CONFIG__ || {}, {
              defaultBackendURL: location.origin,
              defaultBackendSecret: cfxSecret
            });
            if (localStorage.getItem('onboardingDismissed') === null) {
              localStorage.setItem('onboardingDismissed', 'true');
            }
            if ((cfxTheme === 'dark' || cfxTheme === 'light') &&
                (localStorage.getItem('theme') === null || localStorage.getItem('__cfx_theme_synced') !== cfxTheme)) {
              var mapped = cfxTheme === 'light' ? 'winter' : 'night';
              localStorage.setItem('theme', mapped);
              localStorage.setItem('__cfx_theme_synced', cfxTheme);
            }
            if ((cfxLang === 'zh' || cfxLang === 'en') && localStorage.getItem('__cfx_lang_synced') !== cfxLang) {
              document.cookie = 'metacubexd_lang=' + cfxLang + '; path=/; max-age=31536000';
              localStorage.setItem('__cfx_lang_synced', cfxLang);
            }
          } catch (_) {}
        \(compatJs)
          var BANNER_ID = 'clashfx-stale-rules-banner';
          var NOTICE_ID = 'clashfx-guard-notice-banner';
          var rulesSnapshot = null;
          var rulePayloadByIndex = {};
          var aiLockIndices = [];
          var originalFetch = window.fetch;

          function isEnLang() {
            try {
              return document.cookie.indexOf('metacubexd_lang=en') !== -1 ||
                     localStorage.getItem('__cfx_lang_synced') === 'en';
            } catch (_) {
              return false;
            }
          }
          function showGuardNotice(text) {
            var existing = document.getElementById(NOTICE_ID);
            if (existing) existing.remove();
            var toast = document.createElement('div');
            toast.id = NOTICE_ID;
            toast.textContent = text;
            toast.style.cssText = 'position:fixed;top:12px;left:50%;transform:translateX(-50%);z-index:2147483647;padding:8px 14px;border-radius:8px;background:rgba(185,28,28,0.95);color:#fff;font:600 12px -apple-system,BlinkMacSystemFont,sans-serif;box-shadow:0 4px 12px rgba(0,0,0,0.35);pointer-events:none;';
            (document.body || document.documentElement).appendChild(toast);
            setTimeout(function() { if (toast.parentNode) toast.remove(); }, 2800);
          }
          function showStaleBanner() {
            if (document.getElementById(BANNER_ID)) return;
            var banner = document.createElement('div');
            banner.id = BANNER_ID;
            banner.textContent = isEnLang()
              ? 'Core reloading — showing last valid rules snapshot'
              : '内核重载中，当前显示最近一次规则快照';
            banner.style.cssText = 'position:fixed;top:10px;left:50%;transform:translateX(-50%);z-index:2147483647;padding:7px 12px;border-radius:8px;background:rgba(154,87,0,0.94);color:#fff;font:12px -apple-system,BlinkMacSystemFont,sans-serif;box-shadow:0 2px 8px rgba(0,0,0,0.22);pointer-events:none;';
            (document.body || document.documentElement).appendChild(banner);
          }
          function hideStaleBanner() {
            var banner = document.getElementById(BANNER_ID);
            if (banner) banner.remove();
          }
          function parseBackendRequest(input, init) {
            var method = ((init && init.method) || (typeof input !== 'string' && input.method) || 'GET').toUpperCase();
            var rawURL = (typeof input === 'string') ? input : (input && input.url ? input.url : '');
            try {
              var url = new URL(rawURL, window.location.href);
              if (url.origin !== location.origin) return null;
              return { method: method, pathname: url.pathname.replace(/\\/+$/, '') || '/' };
            } catch (_) {
              return null;
            }
          }
          function isMeaningfulRuleTime(count, value) {
            if (Number(count) <= 0 || typeof value !== 'string') return false;
            var ms = Date.parse(value);
            return isFinite(ms) && ms > 0;
          }
          function indexRuleMetadata(payload) {
            if (!payload || !Array.isArray(payload.rules)) return;
            var nextMap = {};
            var nextAi = [];
            for (var i = 0; i < payload.rules.length; i++) {
              var r = payload.rules[i];
              if (!r || typeof r !== 'object') continue;
              var idx = (typeof r.index === 'number') ? r.index : i;
              var pl = String(r.payload || '');
              nextMap[String(idx)] = pl;
              if (pl === 'clashfx-ai-lock') nextAi.push(String(idx));
            }
            rulePayloadByIndex = nextMap;
            aiLockIndices = nextAi;
          }
          function sanitizeRuleTimes(payload) {
            if (!payload || !Array.isArray(payload.rules)) return false;
            var changed = false;
            for (var i = 0; i < payload.rules.length; i++) {
              var extra = payload.rules[i] && payload.rules[i].extra;
              if (!extra || typeof extra !== 'object') continue;
              if (!isMeaningfulRuleTime(extra.hitCount, extra.hitAt) && Object.prototype.hasOwnProperty.call(extra, 'hitAt')) {
                delete extra.hitAt;
                changed = true;
              }
              if (!isMeaningfulRuleTime(extra.missCount, extra.missAt) && Object.prototype.hasOwnProperty.call(extra, 'missAt')) {
                delete extra.missAt;
                changed = true;
              }
            }
            return changed;
          }

          window.fetch = function(input, init) {
            var reqInfo = parseBackendRequest(input, init);
            if (!reqInfo) return originalFetch.apply(this, arguments);

            if ((reqInfo.pathname === '/upgrade' || reqInfo.pathname === '/restart') &&
                (reqInfo.method === 'POST' || reqInfo.method === 'PUT' || reqInfo.method === 'PATCH')) {
              return Promise.resolve(new Response(JSON.stringify({message: 'Managed by ClashFX'}), {
                status: 403, headers: {'Content-Type': 'application/json'}
              }));
            }

            if (reqInfo.method === 'PATCH' && reqInfo.pathname === '/configs' && window.__CLASHFX_AI_LOCKED__) {
              if (init && typeof init.body === 'string') {
                try {
                  var cfgPatch = JSON.parse(init.body);
                  if (cfgPatch && typeof cfgPatch.mode === 'string' && cfgPatch.mode.toLowerCase() !== 'rule') {
                    var aiMsg = isEnLang()
                      ? 'Outbound mode is locked to Rule while AI Proxy Lock is active'
                      : 'AI 专用锁定开启期间出站模式固定为规则模式';
                    showGuardNotice(aiMsg);
                    return Promise.resolve(new Response(JSON.stringify({message: aiMsg}), {
                      status: 403, headers: {'Content-Type': 'application/json'}
                    }));
                  }
                } catch (_) {}
              }
            }

            if (reqInfo.method === 'PATCH' && reqInfo.pathname === '/rules/disable' && init && typeof init.body === 'string') {
              try {
                var patchMap = JSON.parse(init.body);
                if (patchMap && typeof patchMap === 'object') {
                  var keys = Object.keys(patchMap);
                  var touchedAiVal = null;
                  for (var k = 0; k < keys.length; k++) {
                    var plName = rulePayloadByIndex[keys[k]] || '';
                    if (plName === 'clashfx-internal-direct') {
                      var guardMsg = isEnLang()
                        ? 'ClashFX internal loop-prevention guard cannot be disabled'
                        : '系统防环回守护规则固定开启，不可停用';
                      showGuardNotice(guardMsg);
                      return Promise.resolve(new Response(JSON.stringify({message: guardMsg}), {
                        status: 403, headers: {'Content-Type': 'application/json'}
                      }));
                    }
                    if (plName === 'clashfx-ai-lock') {
                      touchedAiVal = Boolean(patchMap[keys[k]]);
                    }
                  }
                  if (touchedAiVal !== null && aiLockIndices.length > 1) {
                    for (var a = 0; a < aiLockIndices.length; a++) {
                      patchMap[aiLockIndices[a]] = touchedAiVal;
                    }
                    var clonedInit = Object.assign({}, init, { body: JSON.stringify(patchMap) });
                    return originalFetch.call(this, input, clonedInit);
                  }
                }
              } catch (_) {}
            }

            if (reqInfo.method !== 'GET' || reqInfo.pathname !== '/rules') {
              return originalFetch.apply(this, arguments);
            }

            var receiver = this, args = arguments;
            return originalFetch.apply(receiver, args).then(function(response) {
              if (response && response.ok) {
                return response.clone().text().then(function(text) {
                  try {
                    var payload = JSON.parse(text);
                    if (!Array.isArray(payload.rules)) return response;
                    indexRuleMetadata(payload);
                    var changed = sanitizeRuleTimes(payload);
                    var normalized = changed ? JSON.stringify(payload) : text;
                    rulesSnapshot = normalized;
                    hideStaleBanner();
                    if (!changed) return response;
                    var headers = new Headers(response.headers);
                    headers.delete('content-length');
                    return new Response(normalized, {status: response.status, statusText: response.statusText, headers: headers});
                  } catch (_) {
                    return response;
                  }
                });
              }
              if (rulesSnapshot && response && response.status >= 500) {
                showStaleBanner();
                return new Response(rulesSnapshot, {status: 200, headers: {'Content-Type': 'application/json', 'X-ClashFX-Stale-Snapshot': 'rules'}});
              }
              return response;
            }).catch(function(err) {
              if (rulesSnapshot) {
                showStaleBanner();
                return new Response(rulesSnapshot, {status: 200, headers: {'Content-Type': 'application/json', 'X-ClashFX-Stale-Snapshot': 'rules'}});
              }
              throw err;
            });
          };

          var origXhrOpen = XMLHttpRequest.prototype.open;
          XMLHttpRequest.prototype.open = function(method, url) {
            this._cfxBlocked = false;
            try {
              var u = new URL(String(url || ''), window.location.href);
              var pn = u.pathname.replace(/\\/+$/, '') || '/';
              if (u.origin === location.origin &&
                  (pn === '/upgrade' || pn === '/restart') &&
                  /^(POST|PUT|PATCH)$/i.test(String(method || ''))) {
                this._cfxBlocked = true;
              }
            } catch (_) {}
            return origXhrOpen.apply(this, arguments);
          };
          var origXhrSend = XMLHttpRequest.prototype.send;
          XMLHttpRequest.prototype.send = function() {
            if (this._cfxBlocked) {
              Object.defineProperty(this, 'readyState', {get: function() { return 4; }});
              Object.defineProperty(this, 'status', {get: function() { return 403; }});
              Object.defineProperty(this, 'responseText', {get: function() { return '{"message":"Managed by ClashFX"}'; }});
              if (typeof this.onreadystatechange === 'function') this.onreadystatechange();
              if (typeof this.onload === 'function') this.onload();
              return;
            }
            return origXhrSend.apply(this, arguments);
          };

          window.addEventListener('message', function(ev) {
            if (ev.origin !== '\(parentOrigin)' &&
                ev.origin !== 'http://app.clashfx.local' &&
                ev.origin !== 'https://app.clashfx.local') {
              return;
            }
            var d = ev.data;
            if (!d || d.type !== 'clashfx_sync') return;
            window.__CLASHFX_AI_LOCKED__ = Boolean(d.aiLocked);
            try {
              if (d.forceTheme && (d.theme === 'dark' || d.theme === 'light')) {
                var nextT = d.theme === 'light' ? 'winter' : 'night';
                var prevT = localStorage.getItem('theme');
                localStorage.setItem('theme', nextT);
                localStorage.setItem('__cfx_theme_synced', d.theme);
                if (prevT !== nextT) {
                  window.dispatchEvent(new StorageEvent('storage', {
                    key: 'theme', oldValue: prevT, newValue: nextT, storageArea: localStorage
                  }));
                }
                if (document.documentElement) {
                  document.documentElement.setAttribute('data-theme', nextT);
                }
              }
              if (d.forceLang) {
                var lc = d.language === 'en_US' ? 'en' : 'zh';
                if (localStorage.getItem('__cfx_lang_synced') !== lc) {
                  document.cookie = 'metacubexd_lang=' + lc + '; path=/; max-age=31536000';
                  localStorage.setItem('__cfx_lang_synced', lc);
                  location.reload();
                }
              }
            } catch (_) {}
          });
        })();
        """

        let userScript = WKUserScript(
            source: script,
            injectionTime: .atDocumentStart,
            forMainFrameOnly: false
        )
        controller.addUserScript(userScript)
    }
}
