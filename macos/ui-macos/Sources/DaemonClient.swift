import CryptoKit
import Darwin
import Foundation

struct DaemonConfig {
    var pipePath: String
    var dataDir: String
    var binDir: String
    var resourceDir: String
    var dashboardDir: String

    static func resolveDefault() -> DaemonConfig {
        let bundleURL = Bundle.main.bundleURL
        let macOSDir = bundleURL.appendingPathComponent("Contents/MacOS").path
        let resDir = Bundle.main.resourceURL?.path
            ?? bundleURL.appendingPathComponent("Contents/Resources").path
        let dashDir = (resDir as NSString).appendingPathComponent("dashboard")
        let defaultSock = "/tmp/clashfx-\(getuid())/daemon.sock"
        return DaemonConfig(
            pipePath: defaultSock,
            dataDir: "",
            binDir: macOSDir,
            resourceDir: resDir,
            dashboardDir: dashDir
        )
    }
}

final class DaemonClient {
    let config: DaemonConfig

    var onConnectionChanged: ((Bool) -> Void)?
    var onStateChanged: (([String: Any]) -> Void)?

    private let stateLock = NSLock()
    private let rpcQueue = DispatchQueue(label: "com.clashfx.daemon.rpc", attributes: .concurrent)
    private let subQueue = DispatchQueue(label: "com.clashfx.daemon.subscribe")

    private var running = false
    private var shouldReconnect = true
    private var connected = false
    private var latestSnapshot: [String: Any] = [:]
    private var nextReqID: Int64 = 10
    private var subscribeFd: Int32 = -1
    private var lastSpawnAttempt = Date.distantPast

    init(config: DaemonConfig) {
        self.config = config
    }

    var isConnected: Bool {
        stateLock.lock()
        defer { stateLock.unlock() }
        return connected
    }

    var snapshot: [String: Any] {
        stateLock.lock()
        defer { stateLock.unlock() }
        return latestSnapshot
    }

    func start() {
        stateLock.lock()
        if running {
            stateLock.unlock()
            return
        }
        running = true
        shouldReconnect = true
        stateLock.unlock()

        subQueue.async { [weak self] in
            self?.subscribeLoop()
        }
    }

    func stopReconnect() {
        stateLock.lock()
        shouldReconnect = false
        running = false
        let fd = subscribeFd
        subscribeFd = -1
        stateLock.unlock()

        if fd >= 0 {
            Darwin.shutdown(fd, SHUT_RDWR)
            Darwin.close(fd)
        }
    }

    func call(
        method: String,
        params: [String: Any] = [:],
        timeoutSeconds: Int = 30,
        completion: ((Bool, Any?, String, String) -> Void)? = nil
    ) {
        rpcQueue.async { [weak self] in
            guard let self else { return }
            let result = self.performBlockingRPC(method: method, params: params, timeoutSeconds: timeoutSeconds)
            if let completion {
                DispatchQueue.main.async {
                    completion(result.ok, result.data, result.error, result.errorCode)
                }
            }
        }
    }

    // MARK: - Private Networking

    private func resolvedSocketPath() -> String {
        let raw = config.pipePath.trimmingCharacters(in: .whitespacesAndNewlines)
        let candidate = raw.isEmpty ? "/tmp/clashfx-\(getuid())/daemon.sock" : raw
        if candidate.utf8.count <= 96 {
            return candidate
        }
        let digest = SHA256.hash(data: Data(candidate.utf8))
        let hex = digest.prefix(6).map { String(format: "%02x", $0) }.joined()
        return "/tmp/clashfx-\(getuid())/ep-\(hex).sock"
    }

    private func allocateRequestID() -> Int64 {
        stateLock.lock()
        defer {
            nextReqID += 1
        }
        return nextReqID
    }

    private func dialSocket(receiveTimeoutSec: Int) -> Int32 {
        let sockPath = resolvedSocketPath()
        let utf8 = Array(sockPath.utf8)
        guard !utf8.isEmpty && utf8.count < MemoryLayout.size(ofValue: sockaddr_un().sun_path) else {
            return -1
        }

        let fd = Darwin.socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { return -1 }

        var noSigPipe: Int32 = 1
        _ = Darwin.setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noSigPipe, socklen_t(MemoryLayout<Int32>.size))

        var sndTv = timeval(tv_sec: 5, tv_usec: 0)
        _ = Darwin.setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &sndTv, socklen_t(MemoryLayout<timeval>.size))

        if receiveTimeoutSec > 0 {
            var rcvTv = timeval(tv_sec: receiveTimeoutSec, tv_usec: 0)
            _ = Darwin.setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &rcvTv, socklen_t(MemoryLayout<timeval>.size))
        }

        var addr = sockaddr_un()
        addr.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
        addr.sun_family = sa_family_t(AF_UNIX)
        withUnsafeMutableBytes(of: &addr.sun_path) { rawBuf in
            rawBuf.initializeMemory(as: UInt8.self, repeating: 0)
            for (idx, byte) in utf8.enumerated() where idx + 1 < rawBuf.count {
                rawBuf[idx] = byte
            }
        }

        let res = withUnsafePointer(to: &addr) { ptr -> Int32 in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockPtr in
                Darwin.connect(fd, sockPtr, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        if res != 0 {
            Darwin.close(fd)
            return -1
        }
        return fd
    }

    private func spawnDaemonIfNeeded() {
        stateLock.lock()
        let now = Date()
        if now.timeIntervalSince(lastSpawnAttempt) < 4.0 {
            stateLock.unlock()
            return
        }
        lastSpawnAttempt = now
        stateLock.unlock()

        var candidates: [String] = []
        if !config.binDir.isEmpty {
            candidates.append((config.binDir as NSString).appendingPathComponent("clashfx-daemon"))
        }
        if let execURL = Bundle.main.executableURL {
            candidates.append(execURL.deletingLastPathComponent().appendingPathComponent("clashfx-daemon").path)
        }
        guard let daemonPath = candidates.first(where: { FileManager.default.isExecutableFile(atPath: $0) }) else {
            return
        }

        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: daemonPath)
        var args: [String] = []
        if !config.pipePath.isEmpty {
            args.append(contentsOf: ["-pipe", config.pipePath])
        }
        if !config.dataDir.isEmpty {
            args.append(contentsOf: ["-data-dir", config.dataDir])
        }
        if !config.binDir.isEmpty {
            args.append(contentsOf: ["-bin-dir", config.binDir])
        }
        if !config.resourceDir.isEmpty {
            args.append(contentsOf: ["-resource-dir", config.resourceDir])
        }
        if !config.dashboardDir.isEmpty {
            args.append(contentsOf: ["-dashboard-dir", config.dashboardDir])
        }
        proc.arguments = args
        proc.standardInput = FileHandle.nullDevice
        proc.standardOutput = FileHandle.nullDevice
        proc.standardError = FileHandle.nullDevice
        try? proc.run()
    }

    private func writeAll(fd: Int32, data: Data) -> Bool {
        data.withUnsafeBytes { rawBuf -> Bool in
            guard let base = rawBuf.baseAddress else { return true }
            var written = 0
            while written < data.count {
                let n = Darwin.write(fd, base.advanced(by: written), data.count - written)
                if n <= 0 {
                    if errno == EINTR { continue }
                    return false
                }
                written += n
            }
            return true
        }
    }

    private func performBlockingRPC(
        method: String,
        params: [String: Any],
        timeoutSeconds: Int
    ) -> (ok: Bool, data: Any?, error: String, errorCode: String) {
        var fd = dialSocket(receiveTimeoutSec: timeoutSeconds)
        if fd < 0 {
            spawnDaemonIfNeeded()
            for _ in 0..<8 {
                Thread.sleep(forTimeInterval: 0.15)
                fd = dialSocket(receiveTimeoutSec: timeoutSeconds)
                if fd >= 0 { break }
            }
        }
        guard fd >= 0 else {
            return (false, nil, "Not connected to clashfx-daemon", "")
        }
        defer { Darwin.close(fd) }

        let reqObj: [String: Any] = [
            "id": allocateRequestID(),
            "method": method,
            "params": params,
        ]
        guard var payload = try? JSONSerialization.data(withJSONObject: reqObj, options: []) else {
            return (false, nil, "Failed to serialize RPC request", "")
        }
        payload.append(0x0A)
        guard writeAll(fd: fd, data: payload) else {
            return (false, nil, "Failed to send RPC request to clashfx-daemon", "")
        }

        var lineBuf = Data()
        var chunk = [UInt8](repeating: 0, count: 16384)
        while true {
            let n = Darwin.read(fd, &chunk, chunk.count)
            if n < 0 {
                if errno == EINTR { continue }
                return (false, nil, "RPC read error: \(String(cString: strerror(errno)))", "")
            }
            if n == 0 {
                break
            }
            lineBuf.append(contentsOf: chunk[0..<n])
            if let nlRange = lineBuf.firstRange(of: Data([0x0A])) {
                lineBuf = lineBuf.subdata(in: 0..<nlRange.lowerBound)
                break
            }
        }

        guard !lineBuf.isEmpty,
              let jsonObj = try? JSONSerialization.jsonObject(with: lineBuf, options: []) as? [String: Any]
        else {
            return (false, nil, "Invalid or empty response from clashfx-daemon", "")
        }

        let ok = (jsonObj["ok"] as? Bool) ?? false
        let data = jsonObj["data"]
        let error = (jsonObj["error"] as? String) ?? ""
        let errorCode = (jsonObj["error_code"] as? String) ?? ""
        return (ok, data, error, errorCode)
    }

    private func setConnected(_ value: Bool) {
        stateLock.lock()
        let changed = (connected != value)
        connected = value
        stateLock.unlock()

        if changed {
            DispatchQueue.main.async { [weak self] in
                self?.onConnectionChanged?(value)
            }
        }
    }

    private func subscribeLoop() {
        while true {
            stateLock.lock()
            let keepGoing = shouldReconnect
            stateLock.unlock()
            if !keepGoing { return }

            let fd = dialSocket(receiveTimeoutSec: 0)
            if fd < 0 {
                setConnected(false)
                spawnDaemonIfNeeded()
                Thread.sleep(forTimeInterval: 0.75)
                continue
            }

            stateLock.lock()
            if !shouldReconnect {
                stateLock.unlock()
                Darwin.close(fd)
                return
            }
            subscribeFd = fd
            stateLock.unlock()

            let subReq = "{\"id\":1,\"method\":\"subscribe\"}\n"
            if let data = subReq.data(using: .utf8), writeAll(fd: fd, data: data) {
                setConnected(true)
                readSubscribeStream(fd: fd)
            }

            stateLock.lock()
            if subscribeFd == fd {
                subscribeFd = -1
            }
            let stillActive = shouldReconnect
            stateLock.unlock()
            Darwin.close(fd)
            setConnected(false)

            if !stillActive { return }
            Thread.sleep(forTimeInterval: 0.8)
        }
    }

    private func readSubscribeStream(fd: Int32) {
        var buffer = Data()
        var chunk = [UInt8](repeating: 0, count: 32768)
        while true {
            let n = Darwin.read(fd, &chunk, chunk.count)
            if n < 0 {
                if errno == EINTR { continue }
                return
            }
            if n == 0 {
                return
            }
            buffer.append(contentsOf: chunk[0..<n])
            while let nlRange = buffer.firstRange(of: Data([0x0A])) {
                let lineData = buffer.subdata(in: 0..<nlRange.lowerBound)
                buffer.removeSubrange(0..<nlRange.upperBound)
                guard !lineData.isEmpty,
                      let obj = try? JSONSerialization.jsonObject(with: lineData, options: []) as? [String: Any]
                else {
                    continue
                }
                handleStreamObject(obj)
            }
        }
    }

    private func handleStreamObject(_ obj: [String: Any]) {
        var nextState: [String: Any]?
        if let event = obj["event"] as? String, event == "state_changed",
           let st = obj["state"] as? [String: Any] {
            nextState = st
        } else if (obj["ok"] as? Bool) == true, let st = obj["data"] as? [String: Any],
                  st["settings"] != nil {
            nextState = st
        }

        guard let snapshotDict = nextState else { return }
        stateLock.lock()
        latestSnapshot = snapshotDict
        stateLock.unlock()

        DispatchQueue.main.async { [weak self] in
            self?.onStateChanged?(snapshotDict)
        }
    }
}
