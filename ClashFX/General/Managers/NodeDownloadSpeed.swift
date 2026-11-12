import Foundation

struct NodeDownloadSpeedTarget: Equatable {
    let name: String
    let port: Int
}

enum NodeDownloadSpeedSample: Equatable {
    case waiting, measuring(Double?), measured(Double), failed, cancelled
}

enum NodeDownloadSpeedPolicy {
    static let maximumBytes = 20_000_000
    static let duration: TimeInterval = 8
    static let testURL = URL(string: "https://speed.cloudflare.com/__down?bytes=20000000")!

    static func targets(state: NodeServiceState, profile: String) -> [NodeDownloadSpeedTarget] {
        guard state.settings.portsEnabled else { return [] }
        let active = Set(state.activeNames[profile] ?? [])
        return (state.registry.profiles[profile] ?? []).filter { $0.enabled && active.contains($0.name) }
            .sorted { $0.port < $1.port }.map { NodeDownloadSpeedTarget(name: $0.name, port: $0.port) }
    }

    static func megabytesPerSecond(bytes: Int64, elapsed: TimeInterval) -> Double? {
        guard bytes > 0, elapsed.isFinite, elapsed > 0 else { return nil }
        return Double(bytes) / max(elapsed, 0.01) / 1_000_000
    }

    static func proxySettings(port: Int) -> [String: Any] {
        ["HTTPEnable": 1, "HTTPProxy": "127.0.0.1", "HTTPPort": port,
         "HTTPSEnable": 1, "HTTPSProxy": "127.0.0.1", "HTTPSPort": port,
         "SOCKSEnable": 0, "ProxyAutoConfigEnable": 0, "ProxyAutoDiscoveryEnable": 0,
         "ExceptionsList": [String](), "ExcludeSimpleHostnames": 0]
    }
}

protocol NodeDownloadSpeedOperation: AnyObject {
    func start(progress: @escaping (Double) -> Void, completion: @escaping (NodeDownloadSpeedSample) -> Void)
    func cancel()
}

/// Delegate callbacks, deadlines and state changes all run on the main queue.
/// Response bytes are counted and discarded, never accumulated into a file/Data.
final class NodeDownloadSpeedProbe: NSObject, NodeDownloadSpeedOperation, URLSessionDataDelegate {
    private let port: Int
    private let url: URL
    private let maximumBytes: Int64
    private let duration: TimeInterval
    private var session: URLSession?
    private var task: URLSessionDataTask?
    private var deadline: DispatchWorkItem?
    private var progress: ((Double) -> Void)?
    private var completion: ((NodeDownloadSpeedSample) -> Void)?
    private var bodyStarted: TimeInterval?
    private var lastProgress: TimeInterval = 0
    private var bytes: Int64 = 0
    private var finished = false

    init(port: Int, url: URL = NodeDownloadSpeedPolicy.testURL,
         maximumBytes: Int = NodeDownloadSpeedPolicy.maximumBytes,
         duration: TimeInterval = NodeDownloadSpeedPolicy.duration) {
        self.port = port
        self.url = url
        self.maximumBytes = Int64(maximumBytes)
        self.duration = duration
        super.init()
    }

    func start(progress: @escaping (Double) -> Void, completion: @escaping (NodeDownloadSpeedSample) -> Void) {
        precondition(Thread.isMainThread)
        guard session == nil, !finished else { return }
        self.progress = progress
        self.completion = completion
        let configuration = URLSessionConfiguration.ephemeral
        configuration.connectionProxyDictionary = NodeDownloadSpeedPolicy.proxySettings(port: port)
        configuration.urlCache = nil
        configuration.httpCookieStorage = nil
        configuration.httpShouldSetCookies = false
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        configuration.timeoutIntervalForRequest = duration
        configuration.timeoutIntervalForResource = duration + 1
        let session = URLSession(configuration: configuration, delegate: self, delegateQueue: .main)
        self.session = session
        var request = URLRequest(url: url, cachePolicy: .reloadIgnoringLocalCacheData, timeoutInterval: duration)
        request.setValue("identity", forHTTPHeaderField: "Accept-Encoding")
        request.setValue("no-store", forHTTPHeaderField: "Cache-Control")
        let task = session.dataTask(with: request)
        self.task = task
        let deadline = DispatchWorkItem { [weak self] in self?.finishWithMeasurement() }
        self.deadline = deadline
        DispatchQueue.main.asyncAfter(deadline: .now() + duration, execute: deadline)
        task.resume()
    }

    func cancel() {
        precondition(Thread.isMainThread)
        finish(.cancelled)
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse,
                    completionHandler: @escaping (URLSession.ResponseDisposition) -> Void) {
        guard !finished, let response = response as? HTTPURLResponse, response.statusCode == 200,
              response.mimeType?.lowercased() != "text/html",
              response.value(forHTTPHeaderField: "Content-Encoding").map({ $0.lowercased() == "identity" }) ?? true else {
            completionHandler(.cancel)
            finish(.failed)
            return
        }
        bodyStarted = ProcessInfo.processInfo.systemUptime
        completionHandler(.allow)
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        guard !finished, let started = bodyStarted else { return }
        bytes += Int64(data.count)
        let now = ProcessInfo.processInfo.systemUptime
        if now - lastProgress >= 0.25, let speed = NodeDownloadSpeedPolicy.megabytesPerSecond(bytes: bytes, elapsed: now - started) {
            lastProgress = now
            progress?(speed)
        }
        guard !finished else { return } // A progress callback can cancel/switch profile.
        if bytes >= maximumBytes { finishWithMeasurement() }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        guard !finished else { return }
        if error == nil || (error as? URLError)?.code == .timedOut {
            finishWithMeasurement()
        } else {
            finish(.failed)
        }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        // Test payloads should not redirect to login pages, other hosts or cleartext.
        completionHandler(nil)
        finish(.failed)
    }

    private func finishWithMeasurement() {
        guard let started = bodyStarted,
              let speed = NodeDownloadSpeedPolicy.megabytesPerSecond(
                bytes: bytes, elapsed: ProcessInfo.processInfo.systemUptime - started
              ) else { finish(.failed); return }
        finish(.measured(speed))
    }

    private func finish(_ result: NodeDownloadSpeedSample) {
        guard !finished else { return }
        finished = true
        deadline?.cancel()
        deadline = nil
        task?.cancel()
        task = nil
        let session = session
        self.session = nil
        session?.invalidateAndCancel()
        progress = nil
        let completion = completion
        self.completion = nil
        completion?(result)
    }
}

/// Serial downloads give each node the available bandwidth. Cancellation/scope
/// changes fence late progress and completions before another node can start.
final class NodeDownloadSpeedRunner {
    typealias Factory = (NodeDownloadSpeedTarget) -> NodeDownloadSpeedOperation
    private let makeProbe: Factory
    private var operation: NodeDownloadSpeedOperation?
    private var pending = [NodeDownloadSpeedTarget]()
    private var generation = UUID()
    private var isCurrent: () -> Bool = { true }
    private(set) var isRunning = false
    private(set) var samples = [String: NodeDownloadSpeedSample]()
    var didChange: (() -> Void)?

    init(makeProbe: @escaping Factory = { NodeDownloadSpeedProbe(port: $0.port) }) { self.makeProbe = makeProbe }

    func start(targets: [NodeDownloadSpeedTarget], isCurrent: @escaping () -> Bool) {
        cancel(clearResults: true)
        guard !targets.isEmpty, isCurrent() else { return }
        generation = UUID()
        self.isCurrent = isCurrent
        pending = targets
        samples = Dictionary(uniqueKeysWithValues: targets.map { ($0.name, .waiting) })
        isRunning = true
        advance(ticket: generation)
    }

    func validate() {
        if isRunning, !isCurrent() { cancel(clearResults: true) }
    }

    func cancel(clearResults: Bool = false) {
        generation = UUID()
        isRunning = false
        pending = []
        let operation = operation
        self.operation = nil
        operation?.cancel()
        if clearResults { samples = [:] }
        else {
            samples = samples.mapValues {
                switch $0 {
                case .waiting, .measuring: return .cancelled
                default: return $0
                }
            }
        }
        didChange?()
    }

    private func advance(ticket: UUID) {
        guard generation == ticket, isRunning else { return }
        guard isCurrent() else { cancel(clearResults: true); return }
        guard !pending.isEmpty else {
            isRunning = false
            didChange?()
            return
        }
        let target = pending.removeFirst()
        let probe = makeProbe(target)
        operation = probe
        samples[target.name] = .measuring(nil)
        didChange?()
        guard generation == ticket, isRunning else { return }
        probe.start(progress: { [weak self] speed in
            guard let self, self.generation == ticket, self.isRunning else { return }
            guard self.isCurrent() else { self.cancel(clearResults: true); return }
            self.samples[target.name] = .measuring(speed)
            self.didChange?()
        }, completion: { [weak self] result in
            guard let self, self.generation == ticket, self.isRunning else { return }
            guard self.isCurrent() else { self.cancel(clearResults: true); return }
            self.operation = nil
            self.samples[target.name] = result
            self.didChange?()
            self.advance(ticket: ticket)
        })
    }
}
