import Cocoa
import Darwin
import Network

final class NodeServices {
    static let shared = NodeServices()
    static let didChange = Notification.Name("ClashFX.NodeServicesDidChange")
    private let store: NodeServiceStore
    private let pathMonitor = NWPathMonitor()
    private var pathMonitorStarted = false
    private var timer: Timer?
    private var online = false
    private var lastCheck = Date.distantPast
    private var lastProfile = ""
    private var policy = NodeHealthRecoveryPolicy()
    private var session: ApiRequest.BenchmarkSession?
    private var generation = UUID()
    private(set) var isChecking = false
    private(set) var isRefreshing = false
    private(set) var status = NSLocalizedString("Waiting for the next node check", comment: "")
    private(set) var samples: [String: NodeHealthSample] = [:]
    private let downloadRunner: NodeDownloadSpeedRunner
    private var downloadProfile = ""
    private var reloadObserver: NSObjectProtocol?
    var isSpeedTesting: Bool { downloadRunner.isRunning }

    init(store: NodeServiceStore = .shared, downloadRunner: NodeDownloadSpeedRunner = .init()) {
        self.store = store
        self.downloadRunner = downloadRunner
        downloadRunner.didChange = { [weak self] in
            guard let self else { return }
            if self.isSpeedTesting {
                self.setStatus(NSLocalizedString("Testing enabled nodes' download speeds…", comment: ""))
            } else {
                self.setStatus(NSLocalizedString("Download speed test finished", comment: ""))
            }
        }
    }

    static func profileKey(_ name: String = ConfigManager.selectConfigName) -> String {
        (ICloudManager.shared.useiCloud.value ? "icloud:" : "local:") + name
    }

    func start() {
        guard timer == nil else { return }
        if reloadObserver == nil {
            reloadObserver = NotificationCenter.default.addObserver(forName: .reloadDashboard, object: nil, queue: .main) { [weak self] _ in
                self?.cancelSpeedTest(clearResults: true)
            }
        }
        pathMonitor.pathUpdateHandler = { [weak self] path in
            self?.updateConnectivity(isOnline: path.status == .satisfied)
        }
        if !pathMonitorStarted {
            pathMonitor.start(queue: .main)
            pathMonitorStarted = true
        }
        let timer = Timer(timeInterval: 5, repeats: true) { [weak self] _ in self?.tick() }
        RunLoop.main.add(timer, forMode: .common)
        self.timer = timer
    }

    func stop() {
        timer?.invalidate()
        timer = nil
        cancelCheck()
        // Keep the path monitor alive when a quit operation is cancelled.
    }

    func updateConnectivity(isOnline: Bool) {
        online = isOnline
        if !isOnline {
            cancelCheck()
            policy = NodeHealthRecoveryPolicy()
            samples = [:]
            setStatus(NSLocalizedString("Network offline; node checks paused", comment: ""))
        }
    }

    func cancelCheck() {
        cancelSpeedTest()
        generation = UUID()
        session?.cancel()
        session = nil
        isChecking = false
    }

    private func tick() {
        downloadRunner.validate()
        guard !isSpeedTesting else { return }
        guard let state = try? store.read(), state.settings.recoveryEnabled,
              Date().timeIntervalSince(lastCheck) >= state.settings.checkInterval else { return }
        checkNow()
    }

    func checkNow() {
        guard online, AppDelegate.shared.nodeServiceChecksAllowed, !isChecking, !isRefreshing, !isSpeedTesting,
              let state = try? store.read() else { return }
        let profile = Self.profileKey()
        let names = state.activeNames[profile] ?? []
        guard !names.isEmpty else {
            setStatus(NSLocalizedString("The active configuration has no proxy nodes", comment: ""))
            return
        }
        if lastProfile != profile {
            policy = NodeHealthRecoveryPolicy()
            samples = [:]
            lastProfile = profile
        }
        isChecking = true
        lastCheck = Date()
        let ticket = UUID()
        generation = ticket
        let context = ApiRequest.runtimeConfigContext()
        let session = ApiRequest.BenchmarkSession()
        self.session = session
        setStatus(NSLocalizedString("Checking node connectivity…", comment: ""))
        var next = 0
        var running = 0
        var outcomes = [String: NodeHealthSample]()

        func current() -> Bool {
            self.generation == ticket && !session.isCancelled && profile == Self.profileKey() &&
                context == ApiRequest.runtimeConfigContext() && AppDelegate.shared.nodeServiceChecksAllowed && self.online
        }
        func schedule() {
            guard current() else {
                if self.generation == ticket { self.cancelCheck() }
                return
            }
            while running < 3, next < names.count {
                let name = names[next]
                next += 1
                running += 1
                self.probe(name, session: session) { sample in
                    guard current() else {
                        if self.generation == ticket { self.cancelCheck() }
                        return
                    }
                    outcomes[name] = sample
                    running -= 1
                    schedule()
                }
            }
            if running == 0, next == names.count {
                self.session = nil
                self.isChecking = false
                self.samples = outcomes
                let failed = self.policy.record(outcomes, threshold: state.settings.failureThreshold)
                self.setStatus(NSLocalizedString("Node connectivity check finished", comment: ""))
                if !failed.isEmpty, state.settings.recoveryEnabled {
                    self.refreshSubscription(manual: false)
                }
            }
        }
        schedule()
    }

    private func probe(_ name: String, session: ApiRequest.BenchmarkSession,
                       secondary: Bool = false, completion: @escaping (NodeHealthSample) -> Void) {
        let url = secondary ? "https://cp.cloudflare.com/generate_204" : "https://www.gstatic.com/generate_204"
        ApiRequest.getProxyDelayOutcome(proxyName: name, benchmarkURL: url, timeout: 5000,
                                        session: session, expectedStatus: "204") { [weak self] outcome in
            DispatchQueue.main.async {
                guard let self, !session.isCancelled else { completion(.unavailable); return }
                switch outcome {
                case let .measured(delay): completion(.healthy(delay))
                case .failed, .timedOut:
                    if secondary { completion(.failed) }
                    else { self.probe(name, session: session, secondary: true, completion: completion) }
                case .unavailable, .cancelled: completion(.unavailable)
                }
            }
        }
    }

    func refreshSubscription(manual: Bool = true) {
        guard online, AppDelegate.shared.nodeServiceChecksAllowed, !isChecking, !isRefreshing, !isSpeedTesting,
              var state = try? store.read(), state.settings.recoveryEnabled else { return }
        let profile = Self.profileKey()
        guard manual || NodeHealthRecoveryPolicy.canRefresh(now: Date(), lastAttempt: state.lastRefresh[profile],
                                                            cooldown: state.settings.refreshCooldown) else {
            setStatus(NSLocalizedString("Node failure detected; subscription refresh is cooling down", comment: ""))
            return
        }
        guard let config = RemoteConfigManager.shared.configs.first(where: { $0.name == ConfigManager.selectConfigName }) else {
            setStatus(NSLocalizedString("No managed subscription for the active configuration", comment: ""))
            return
        }
        guard !config.updating else { return }
        state.lastRefresh[profile] = Date()
        do { try store.write(state) } catch {
            setStatus(error.localizedDescription)
            return
        }
        isRefreshing = true
        config.updating = true
        setStatus(NSLocalizedString("Refreshing subscription through DIRECT…", comment: ""))
        Logger.log("[Node Recovery] Direct refresh started for \(config.name)")
        RemoteConfigManager.updateConfig(config: config, forceDirect: true) { [weak self] error in
            config.updating = false
            RemoteConfigManager.shared.saveConfigs()
            guard let self else { return }
            guard profile == Self.profileKey(), !AppDelegate.shared.isTerminating else {
                self.isRefreshing = false
                return
            }
            if error != nil {
                self.isRefreshing = false
                self.setStatus(NSLocalizedString("Direct refresh failed; previous configuration retained", comment: ""))
                Logger.log("[Node Recovery] Direct refresh failed for \(config.name)", level: .warning)
                return
            }
            AppDelegate.shared.updateConfig(showNotification: false, showError: false) { error in
                self.isRefreshing = false
                guard profile == Self.profileKey() else { return }
                if error == nil {
                    self.policy = NodeHealthRecoveryPolicy()
                    self.lastCheck = .distantPast
                    self.setStatus(NSLocalizedString("Subscription refreshed directly and reloaded", comment: ""))
                } else {
                    self.setStatus(NSLocalizedString("Subscription downloaded; runtime reload failed", comment: ""))
                }
            }
        }
    }

    func statusForNode(_ name: String) -> String {
        switch samples[name] {
        case let .healthy(delay): return "\(delay) ms"
        case .failed: return String(format: NSLocalizedString("Unreachable (%d consecutive failures)", comment: ""), policy.failures[name, default: 0])
        case .unavailable: return NSLocalizedString("Check unavailable", comment: "")
        case nil: return NSLocalizedString("Not checked", comment: "")
        }
    }

    func testDownloadSpeeds() {
        guard online, AppDelegate.shared.nodeServiceChecksAllowed, !isRefreshing, !isSpeedTesting,
              let state = try? store.read() else { return }
        let profile = Self.profileKey()
        let targets = NodeDownloadSpeedPolicy.targets(state: state, profile: profile)
        guard !targets.isEmpty else {
            setStatus(NSLocalizedString("Enable and save at least one node before testing download speed", comment: ""))
            return
        }
        cancelCheck()
        downloadProfile = profile
        let context = ApiRequest.runtimeConfigContext()
        downloadRunner.start(targets: targets, isCurrent: { [weak self] in
            guard let self, self.online, profile == Self.profileKey(),
                  context == ApiRequest.runtimeConfigContext(), AppDelegate.shared.nodeServiceChecksAllowed,
                  let current = try? self.store.read() else { return false }
            return NodeDownloadSpeedPolicy.targets(state: current, profile: profile) == targets
        })
    }

    func cancelSpeedTest(clearResults: Bool = false) {
        guard isSpeedTesting || (clearResults && !downloadRunner.samples.isEmpty) else { return }
        downloadRunner.cancel(clearResults: clearResults)
        if clearResults { downloadProfile = "" }
    }

    func downloadSpeedForNode(_ name: String) -> String {
        guard downloadProfile == Self.profileKey() else { return "—" }
        switch downloadRunner.samples[name] {
        case .waiting: return NSLocalizedString("Waiting", comment: "")
        case .measuring(nil): return NSLocalizedString("Testing…", comment: "")
        case let .measuring(speed?): return String(format: NSLocalizedString("Testing… %.2f MB/s", comment: ""), speed)
        case let .measured(speed): return String(format: "%.2f MB/s", speed)
        case .failed: return NSLocalizedString("Failed", comment: "")
        case .cancelled: return NSLocalizedString("Stopped", comment: "")
        case nil: return "—"
        }
    }

    private func setStatus(_ text: String) {
        status = text
        NotificationCenter.default.post(name: Self.didChange, object: nil)
    }

    static func portAvailable(_ port: Int) -> Bool {
        for kind in [SOCK_STREAM, SOCK_DGRAM] {
            let fd = Darwin.socket(AF_INET, kind, 0)
            guard fd >= 0 else { return false }
            defer { Darwin.close(fd) }
            var address = sockaddr_in()
            address.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
            address.sin_family = sa_family_t(AF_INET)
            address.sin_port = in_port_t(port).bigEndian
            address.sin_addr.s_addr = inet_addr("127.0.0.1")
            let result = withUnsafePointer(to: &address) {
                $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
            }
            if result != 0 { return false }
        }
        return true
    }
}
