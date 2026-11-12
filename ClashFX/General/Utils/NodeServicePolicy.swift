import Foundation

struct NodeServiceSettings: Codable, Equatable {
    var portsEnabled = true
    var recoveryEnabled = true
    var firstPort = 21000
    var checkInterval: TimeInterval = 60
    var failureThreshold = 3
    var refreshCooldown: TimeInterval = 300

    func validate() throws {
        guard (1024 ... 65000).contains(firstPort), (15 ... 3600).contains(checkInterval),
              (2 ... 10).contains(failureThreshold), (60 ... 86400).contains(refreshCooldown) else {
            throw NodeServiceError.invalidSettings
        }
    }
}

enum NodeServiceError: LocalizedError {
    case invalidSettings, noFreePort, invalidNodes, corruptState
    case portConflict(Int)

    var errorDescription: String? {
        switch self {
        case .invalidSettings: return "Use ports 1024–65000, intervals 15–3600 seconds, 2–10 failures and cooldowns 60–86400 seconds."
        case .noFreePort: return "No free local port is available."
        case .invalidNodes: return "Proxy names must be nonempty and unique."
        case .corruptState: return "The saved node port assignments cannot be read. They have not been overwritten."
        case let .portConflict(port): return "Port \(port) is reserved or assigned to another node. Choose another port."
        }
    }
}

struct NodePortBinding: Codable, Equatable {
    var name: String
    var serverKey: String
    var port: Int
    var enabled: Bool

    init(name: String, serverKey: String, port: Int, enabled: Bool = true) {
        self.name = name
        self.serverKey = serverKey
        self.port = port
        self.enabled = enabled
    }

    private enum CodingKeys: String, CodingKey { case name, serverKey, port, enabled }

    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        name = try values.decode(String.self, forKey: .name)
        serverKey = try values.decode(String.self, forKey: .serverKey)
        port = try values.decode(Int.self, forKey: .port)
        // Earlier builds opened every assigned port. Preserve that behavior.
        enabled = try values.decodeIfPresent(Bool.self, forKey: .enabled) ?? true
    }
}

struct NodePortRegistry: Codable, Equatable {
    var profiles: [String: [NodePortBinding]] = [:]
    var directPort: Int?

    mutating func reconcile(profile: String, proxies: [[String: Any]], firstPort: Int,
                            reserved: Set<Int>, available: (Int) -> Bool) throws -> [NodePortBinding] {
        let names = proxies.compactMap { $0["name"] as? String }
        guard names.count == proxies.count, Set(names).count == names.count,
              names.allSatisfy({ !$0.isEmpty }) else { throw NodeServiceError.invalidNodes }
        let previous = profiles[profile] ?? []
        var retained = previous
        var used = Set(profiles.values.flatMap { $0 }.map { $0.port }).union(reserved)
        if let port = directPort { used.insert(port) }
        var claimed = Set<String>()
        var active = [NodePortBinding]()
        let serverKeys = proxies.map(Self.serverKey)

        for (index, name) in names.enumerated() {
            let key = serverKeys[index]
            var binding = previous.first { $0.name == name }
            // Password rotations and list ordering never affect a port. Also
            // retain it across a rename/server-port change when that server is
            // unambiguous, as in JMS's one-node-per-server subscriptions.
            if binding == nil, !key.isEmpty, serverKeys.filter({ $0 == key }).count == 1 {
                let candidates = previous.filter {
                    $0.serverKey == key && !names.contains($0.name) && !claimed.contains($0.name)
                }
                if candidates.count == 1 {
                    binding = candidates[0]
                    retained.removeAll { $0.name == candidates[0].name }
                }
            }
            if var existing = binding {
                guard (!existing.enabled || !reserved.contains(existing.port)), existing.port != directPort else {
                    throw NodeServiceError.portConflict(existing.port)
                }
                claimed.insert(existing.name)
                existing.name = name
                existing.serverKey = key
                retained.removeAll { $0.name == name }
                retained.append(existing)
                active.append(existing)
            } else {
                guard let port = (firstPort ... 65535).first(where: { !used.contains($0) && available($0) }) else {
                    throw NodeServiceError.noFreePort
                }
                used.insert(port)
                let entry = NodePortBinding(name: name, serverKey: key, port: port)
                retained.append(entry)
                active.append(entry)
            }
        }
        profiles[profile] = retained
        return active
    }

    mutating func setPort(_ port: Int, profile: String, node: String) throws {
        guard (1024 ... 65535).contains(port), port != directPort else { throw NodeServiceError.portConflict(port) }
        for (scope, entries) in profiles {
            if entries.contains(where: { $0.port == port && (scope != profile || $0.name != node) }) {
                throw NodeServiceError.portConflict(port)
            }
        }
        guard let index = profiles[profile]?.firstIndex(where: { $0.name == node }) else {
            throw NodeServiceError.invalidNodes
        }
        profiles[profile]?[index].port = port
    }

    mutating func setPorts(_ changes: [String: Int], profile: String) throws {
        var next = self
        for (name, port) in changes {
            guard (1024 ... 65535).contains(port), port != directPort,
                  let index = next.profiles[profile]?.firstIndex(where: { $0.name == name }) else {
                throw NodeServiceError.portConflict(port)
            }
            next.profiles[profile]?[index].port = port
        }
        let ports = next.profiles.values.flatMap { $0 }.map { $0.port }
        guard Set(ports).count == ports.count else { throw NodeServiceError.portConflict(changes.values.first ?? 0) }
        self = next
    }

    mutating func setEnabled(_ changes: [String: Bool], profile: String) throws {
        var next = self
        for (name, enabled) in changes {
            guard let index = next.profiles[profile]?.firstIndex(where: { $0.name == name }) else {
                throw NodeServiceError.invalidNodes
            }
            next.profiles[profile]?[index].enabled = enabled
        }
        self = next
    }

    mutating func prepareDirectPort(reserved: Set<Int>, available: (Int) -> Bool) throws -> Int {
        if let port = directPort {
            guard !reserved.contains(port) else { throw NodeServiceError.portConflict(port) }
            return port
        }
        let occupied = Set(profiles.values.flatMap { $0 }.map { $0.port }).union(reserved)
        guard let port = (20000 ... 20999).reversed().first(where: { !occupied.contains($0) && available($0) }) else {
            throw NodeServiceError.noFreePort
        }
        directPort = port
        return port
    }

    private static func serverKey(_ proxy: [String: Any]) -> String {
        guard let server = proxy["server"] as? String, !server.isEmpty else { return "" }
        return "\((proxy["type"] as? String ?? "").lowercased())|\(server.lowercased())"
    }
}

enum NodeListenerPolicy {
    static let prefix = "clashfx-node-service-"

    static func reservedPorts(in root: [String: Any]) -> Set<Int> {
        var ports = Set(["port", "socks-port", "mixed-port", "redir-port", "tproxy-port"].compactMap { root[$0] as? Int })
        if let controller = root["external-controller"] as? String,
           let port = controller.split(separator: ":").last.flatMap({ Int($0) }) { ports.insert(port) }
        for listener in root["listeners"] as? [[String: Any]] ?? [] {
            guard !(listener["name"] as? String ?? "").hasPrefix(prefix) else { continue }
            if let port = listener["port"] as? Int { ports.insert(port) }
            if let value = listener["port"] as? String {
                for part in value.split(separator: "/") {
                    let ends = part.split(separator: "-").compactMap { Int($0) }
                    if ends.count == 1 { ports.insert(ends[0]) }
                    if ends.count == 2, ends[0] <= ends[1], ends[1] <= 65535 { ports.formUnion(ends[0] ... ends[1]) }
                }
            }
        }
        return ports
    }

    static func apply(to root: inout [String: Any], bindings: [NodePortBinding], directPort: Int?,
                      protectionRules: [String] = []) {
        var listeners = (root["listeners"] as? [[String: Any]] ?? []).filter {
            !(($0["name"] as? String ?? "").hasPrefix(prefix))
        }
        var subRules = (root["sub-rules"] as? [String: Any] ?? [:]).filter { !$0.key.hasPrefix(prefix) }
        func listener(name: String, port: Int, proxy: String, udp: Bool) -> [String: Any] {
            var entry: [String: Any] = ["name": name, "type": "mixed", "listen": "127.0.0.1", "port": port, "udp": udp]
            if protectionRules.isEmpty {
                entry["proxy"] = proxy
            } else {
                let ruleName = name + "-rules"
                entry["rule"] = ruleName
                subRules[ruleName] = protectionRules + ["MATCH,\(proxy)", "MATCH,REJECT"]
            }
            return entry
        }
        for binding in bindings where binding.enabled {
            listeners.append(listener(name: "\(prefix)\(binding.port)", port: binding.port, proxy: binding.name, udp: true))
        }
        if let port = directPort {
            listeners.append(listener(name: "\(prefix)direct-refresh", port: port, proxy: "DIRECT", udp: false))
        }
        if !listeners.isEmpty || root["listeners"] != nil { root["listeners"] = listeners }
        if !subRules.isEmpty || root["sub-rules"] != nil { root["sub-rules"] = subRules }
    }
}

enum NodeHealthSample: Equatable {
    case healthy(Int), failed, unavailable
}

struct NodeHealthRecoveryPolicy {
    private(set) var failures: [String: Int] = [:]

    mutating func record(_ samples: [String: NodeHealthSample], threshold: Int) -> [String] {
        failures = failures.filter { samples[$0.key] != nil }
        for (name, sample) in samples {
            switch sample {
            case .healthy, .unavailable: failures[name] = 0
            case .failed: failures[name] = (failures[name] ?? 0) + 1
            }
        }
        return failures.keys.filter { failures[$0, default: 0] >= threshold }.sorted()
    }

    static func canRefresh(now: Date, lastAttempt: Date?, cooldown: TimeInterval) -> Bool {
        guard let lastAttempt else { return true }
        return now.timeIntervalSince(lastAttempt) >= cooldown
    }
}

struct NodeServiceState: Codable, Equatable {
    var settings = NodeServiceSettings()
    var registry = NodePortRegistry()
    var lastRefresh: [String: Date] = [:]
    var activeNames: [String: [String]] = [:]
}

/// One atomically encoded defaults value; config generation may run off-main.
final class NodeServiceStore {
    static let shared = NodeServiceStore()
    static let key = "nodeServices.v1"
    private let defaults: UserDefaults
    private let lock = NSRecursiveLock()

    init(defaults: UserDefaults = .standard) { self.defaults = defaults }

    func read() throws -> NodeServiceState {
        lock.lock(); defer { lock.unlock() }
        guard let data = defaults.data(forKey: Self.key) else { return NodeServiceState() }
        guard let state = try? JSONDecoder().decode(NodeServiceState.self, from: data) else {
            throw NodeServiceError.corruptState
        }
        return state
    }

    func write(_ state: NodeServiceState) throws {
        lock.lock(); defer { lock.unlock() }
        try state.settings.validate()
        defaults.set(try JSONEncoder().encode(state), forKey: Self.key)
    }

    @discardableResult
    func prepare(root: inout [String: Any], profile: String,
                 additionalReserved: Set<Int> = [], protectionRules: [String] = [],
                 available: (Int) -> Bool) throws -> [NodePortBinding] {
        lock.lock(); defer { lock.unlock() }
        var state = try read()
        try state.settings.validate()
        let reserved = NodeListenerPolicy.reservedPorts(in: root).union(additionalReserved)
        if let value = root["listeners"], !(value is [[String: Any]]) { throw NodeServiceError.invalidNodes }
        if let value = root["sub-rules"], !(value is [String: Any]) { throw NodeServiceError.invalidNodes }
        let proxies = root["proxies"] as? [[String: Any]] ?? []
        let bindings = state.settings.portsEnabled
            ? try state.registry.reconcile(profile: profile, proxies: proxies, firstPort: state.settings.firstPort,
                                           reserved: reserved, available: available) : []
        state.activeNames[profile] = proxies.compactMap { $0["name"] as? String }
        let direct = state.settings.recoveryEnabled ? try state.registry.prepareDirectPort(reserved: reserved, available: available) : nil
        NodeListenerPolicy.apply(to: &root, bindings: state.settings.portsEnabled ? bindings : [], directPort: direct,
                                 protectionRules: protectionRules)
        try write(state)
        return bindings
    }
}

enum NodeDirectTransportPolicy {
    /// A loopback listener pinned to DIRECT also bypasses remote nodes under
    /// TUN/global mode and across HTTP redirects. Never fall back to the OS's
    /// proxy settings when this listener is unavailable.
    static func proxySettings(port: Int) -> [String: Any] {
        ["HTTPEnable": 1, "HTTPProxy": "127.0.0.1", "HTTPPort": port,
         "HTTPSEnable": 1, "HTTPSProxy": "127.0.0.1", "HTTPSPort": port,
         "SOCKSEnable": 0, "ProxyAutoConfigEnable": 0, "ProxyAutoDiscoveryEnable": 0,
         "ExceptionsList": [String](), "ExcludeSimpleHostnames": 0]
    }
}
