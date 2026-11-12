import Cocoa

final class NodeServicesWindowController: NSWindowController, NSTableViewDataSource, NSTableViewDelegate {
    private static var instance: NodeServicesWindowController?
    private let ports = NSButton(checkboxWithTitle: NSLocalizedString("Enable multiple node ports", comment: ""), target: nil, action: nil)
    private let recovery = NSButton(checkboxWithTitle: NSLocalizedString("Automatically refresh subscription directly after node failures", comment: ""), target: nil, action: nil)
    private let firstPort = NSTextField(string: "21000")
    private let interval = NSTextField(string: "60")
    private let threshold = NSTextField(string: "3")
    private let cooldown = NSTextField(string: "300")
    private let table = NSTableView()
    private let status = NSTextField(wrappingLabelWithString: "")
    private var bindings = [NodePortBinding]()
    private var edits = [String: Int]()
    private var enabledEdits = [String: Bool]()
    private var profile = ""
    private var observers = [NSObjectProtocol]()
    private var saveButton: NSButton!
    private var speedButton: NSButton!
    private var checkButton: NSButton!
    private var refreshButton: NSButton!
    private var saving = false

    static func show() {
        if instance == nil { instance = NodeServicesWindowController() }
        if instance?.window?.isVisible != true { instance?.loadSettings() }
        instance?.showWindow(nil)
        instance?.window?.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    private init() {
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 970, height: 620),
                              styleMask: [.titled, .closable, .miniaturizable, .resizable], backing: .buffered, defer: false)
        window.title = NSLocalizedString("Node Ports & Recovery", comment: "")
        window.minSize = NSSize(width: 950, height: 600)
        window.isReleasedWhenClosed = false
        super.init(window: window)
        buildContent()
        window.center()
        for name in [NodeServices.didChange, .reloadDashboard] {
            observers.append(NotificationCenter.default.addObserver(forName: name, object: nil, queue: .main) { [weak self] _ in
                self?.refreshRows()
            })
        }
    }

    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

    private func buildContent() {
        guard let content = window?.contentView else { return }
        let stack = NSStackView()
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 12
        stack.translatesAutoresizingMaskIntoConstraints = false
        content.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: content.leadingAnchor, constant: 20),
            stack.trailingAnchor.constraint(equalTo: content.trailingAnchor, constant: -20),
            stack.topAnchor.constraint(equalTo: content.topAnchor, constant: 20),
            stack.bottomAnchor.constraint(equalTo: content.bottomAnchor, constant: -20),
        ])
        let toggles = NSStackView(views: [ports,
            NSButton(title: NSLocalizedString("Enable all", comment: ""), target: self, action: #selector(enableAll)),
            NSButton(title: NSLocalizedString("Stop all", comment: ""), target: self, action: #selector(stopAll))])
        toggles.spacing = 14
        ports.target = self
        ports.action = #selector(portsChanged)
        stack.addArrangedSubview(toggles)
        stack.addArrangedSubview(recovery)
        let options = NSStackView()
        options.spacing = 12
        for (label, field) in [("First new port", firstPort), ("Check interval (s)", interval),
                               ("Consecutive failures", threshold), ("Refresh cooldown (s)", cooldown)] {
            let item = NSStackView(views: [NSTextField(labelWithString: NSLocalizedString(label, comment: "")), field])
            item.orientation = .vertical
            item.alignment = .leading
            field.widthAnchor.constraint(equalToConstant: 95).isActive = true
            options.addArrangedSubview(item)
        }
        stack.addArrangedSubview(options)
        let help = NSTextField(wrappingLabelWithString: NSLocalizedString("Check nodes and Save & Reload to run their fixed local ports. Download speed tests run enabled nodes one at a time, using up to 20 MB or 8 seconds per node. Tests consume proxy traffic and can be stopped.", comment: ""))
        help.textColor = .secondaryLabelColor
        stack.addArrangedSubview(help)
        for (key, title, width) in [("enabled", "Enabled", 50.0), ("node", "Node", 400.0), ("port", "Fixed port", 80.0),
                                    ("health", "Connectivity", 160.0), ("speed", "Download speed", 170.0)] {
            let column = NSTableColumn(identifier: NSUserInterfaceItemIdentifier(key))
            column.title = NSLocalizedString(title, comment: "")
            column.width = CGFloat(width)
            column.isEditable = key == "port" || key == "enabled"
            if key == "enabled" {
                let cell = NSButtonCell()
                cell.setButtonType(.switch)
                cell.title = ""
                column.dataCell = cell
            }
            table.addTableColumn(column)
        }
        table.delegate = self
        table.dataSource = self
        table.usesAlternatingRowBackgroundColors = true
        table.allowsMultipleSelection = false
        table.columnAutoresizingStyle = .lastColumnOnlyAutoresizingStyle
        let scroll = NSScrollView()
        scroll.documentView = table
        scroll.hasVerticalScroller = true
        scroll.hasHorizontalScroller = true
        scroll.borderType = .bezelBorder
        stack.addArrangedSubview(scroll)
        scroll.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true
        scroll.heightAnchor.constraint(greaterThanOrEqualToConstant: 160).isActive = true
        scroll.setContentHuggingPriority(.defaultLow, for: .vertical)
        stack.addArrangedSubview(status)
        status.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true
        let actions = NSStackView()
        for (title, selector) in [("Check now", #selector(checkNowAction)), ("Refresh subscription directly", #selector(refreshAction)),
                                  ("Speed test", #selector(speedTest)), ("Save & Reload", #selector(save))] {
            let button = NSButton(title: NSLocalizedString(title, comment: ""), target: self, action: selector)
            actions.addArrangedSubview(button)
            if title == "Save & Reload" { saveButton = button }
            if title == "Speed test" { speedButton = button }
            if title == "Check now" { checkButton = button }
            if title == "Refresh subscription directly" { refreshButton = button }
        }
        stack.addArrangedSubview(actions)
    }

    private func loadSettings() {
        do {
            let settings = try NodeServiceStore.shared.read().settings
            ports.state = settings.portsEnabled ? .on : .off
            recovery.state = settings.recoveryEnabled ? .on : .off
            firstPort.stringValue = String(settings.firstPort)
            interval.stringValue = String(Int(settings.checkInterval))
            threshold.stringValue = String(settings.failureThreshold)
            cooldown.stringValue = String(Int(settings.refreshCooldown))
            edits = [:]
            enabledEdits = [:]
            refreshRows()
        } catch { showError(error.localizedDescription) }
    }

    private func refreshRows() {
        guard let state = try? NodeServiceStore.shared.read() else { return }
        let scope = NodeServices.profileKey()
        if scope != profile { window?.makeFirstResponder(nil); edits = [:]; enabledEdits = [:]; profile = scope }
        guard table.editedRow < 0 else { return }
        let names = Set(state.activeNames[profile] ?? [])
        bindings = (state.registry.profiles[profile] ?? []).filter { names.contains($0.name) }.sorted { $0.port < $1.port }
        let enabledCount = state.settings.portsEnabled ? bindings.filter { $0.enabled }.count : 0
        status.stringValue = "\(ConfigManager.selectConfigName) — " + String(format: NSLocalizedString("%d node ports configured", comment: ""), enabledCount) + " — \(NodeServices.shared.status)"
        if Settings.claudeProxyLockEnabled {
            status.stringValue += "\n" + String(format: NSLocalizedString("AI traffic → %@; other pages use their chosen node ports", comment: ""), Settings.claudeProxyLockTarget)
        }
        table.reloadData()
        speedButton.title = NSLocalizedString(NodeServices.shared.isSpeedTesting ? "Stop speed test" : "Speed test", comment: "")
        speedButton.isEnabled = NodeServices.shared.isSpeedTesting || !NodeServices.shared.isRefreshing
        checkButton.isEnabled = !NodeServices.shared.isSpeedTesting
        refreshButton.isEnabled = !NodeServices.shared.isSpeedTesting
        saveButton.isEnabled = !saving && !NodeServices.shared.isSpeedTesting
    }

    func numberOfRows(in tableView: NSTableView) -> Int { bindings.count }

    func tableView(_ tableView: NSTableView, objectValueFor tableColumn: NSTableColumn?, row: Int) -> Any? {
        guard bindings.indices.contains(row) else { return nil }
        let binding = bindings[row]
        switch tableColumn?.identifier.rawValue {
        case "enabled": return NSNumber(value: enabledEdits[binding.name] ?? binding.enabled)
        case "node": return binding.name
        case "port": return String(edits[binding.name] ?? binding.port)
        case "health": return NodeServices.shared.statusForNode(binding.name)
        case "speed":
            guard enabledEdits[binding.name] ?? binding.enabled else { return "—" }
            return NodeServices.shared.downloadSpeedForNode(binding.name)
        default: return nil
        }
    }

    func tableView(_ tableView: NSTableView, setObjectValue object: Any?, for tableColumn: NSTableColumn?, row: Int) {
        guard bindings.indices.contains(row) else { return }
        guard profile == NodeServices.profileKey() else { refreshRows(); return }
        let binding = bindings[row]
        if tableColumn?.identifier.rawValue == "enabled" {
            NodeServices.shared.cancelSpeedTest()
            enabledEdits[binding.name] = (object as? NSNumber)?.boolValue ?? false
            table.reloadData()
            return
        }
        guard tableColumn?.identifier.rawValue == "port" else { return }
        guard let port = Int(String(describing: object ?? "")), (1024 ... 65535).contains(port) else {
            showError(NSLocalizedString("Enter a port between 1024 and 65535", comment: ""))
            return
        }
        edits[binding.name] = port
        NodeServices.shared.cancelSpeedTest()
    }

    @objc private func save() {
        window?.makeFirstResponder(nil)
        guard profile == NodeServices.profileKey() else { loadSettings(); return }
        guard !NodeServices.shared.isRefreshing, !NodeServices.shared.isSpeedTesting, AppDelegate.shared.nodeServiceChecksAllowed else {
            showError(NSLocalizedString("Wait until the current configuration operation finishes", comment: ""))
            return
        }
        guard let start = Int(firstPort.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)),
              let seconds = Int(interval.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)),
              let failures = Int(threshold.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)),
              let cooling = Int(cooldown.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)) else {
            showError(NodeServiceError.invalidSettings.localizedDescription)
            return
        }
        do {
            let previous = try NodeServiceStore.shared.read()
            var next = previous
            next.settings.portsEnabled = ports.state == .on
            next.settings.recoveryEnabled = recovery.state == .on
            next.settings.firstPort = start
            next.settings.checkInterval = TimeInterval(seconds)
            next.settings.failureThreshold = failures
            next.settings.refreshCooldown = TimeInterval(cooling)
            try next.registry.setPorts(edits, profile: profile)
            try next.registry.setEnabled(enabledEdits, profile: profile)
            try NodeServiceStore.shared.write(next)
            NodeServices.shared.cancelCheck()
            saving = true
            saveButton.isEnabled = false
            AppDelegate.shared.updateConfig(showNotification: false) { [weak self] error in
                self?.saving = false
                self?.saveButton.isEnabled = true
                if let error {
                    try? NodeServiceStore.shared.write(previous)
                    self?.showError(error)
                } else {
                    self?.edits = [:]
                    self?.enabledEdits = [:]
                    self?.refreshRows()
                }
            }
        } catch { showError(error.localizedDescription) }
    }

    @objc private func checkNowAction() { NodeServices.shared.checkNow() }
    @objc private func refreshAction() { NodeServices.shared.refreshSubscription() }
    @objc private func portsChanged() { NodeServices.shared.cancelSpeedTest() }
    @objc private func speedTest() {
        if NodeServices.shared.isSpeedTesting { NodeServices.shared.cancelSpeedTest(); return }
        window?.makeFirstResponder(nil)
        do {
            let state = try NodeServiceStore.shared.read()
            guard ports.state == (state.settings.portsEnabled ? .on : .off),
                  bindings.allSatisfy({ (edits[$0.name] ?? $0.port) == $0.port &&
                      (enabledEdits[$0.name] ?? $0.enabled) == $0.enabled }) else {
                showError(NSLocalizedString("Save & Reload node changes before testing download speed", comment: ""))
                return
            }
            NodeServices.shared.testDownloadSpeeds()
        } catch { showError(error.localizedDescription) }
    }
    @objc private func enableAll() {
        NodeServices.shared.cancelSpeedTest()
        ports.state = .on
        for binding in bindings { enabledEdits[binding.name] = true }
        table.reloadData()
    }
    @objc private func stopAll() {
        NodeServices.shared.cancelSpeedTest()
        for binding in bindings { enabledEdits[binding.name] = false }
        table.reloadData()
    }

    private func showError(_ text: String) {
        let alert = NSAlert()
        alert.messageText = NSLocalizedString("Node Ports & Recovery", comment: "")
        alert.informativeText = text
        alert.alertStyle = .warning
        alert.runModal()
    }
}
