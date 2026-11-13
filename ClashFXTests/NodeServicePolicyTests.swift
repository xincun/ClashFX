import XCTest

final class NodeServicePolicyTests: XCTestCase {
    private func node(_ name: String, server: String? = nil) -> [String: Any] {
        ["name": name, "type": "vless", "server": server ?? name, "port": 443, "uuid": "secret"]
    }

    func testPortsSurviveReorderingPasswordRotationAndTemporaryRemoval() throws {
        var registry = NodePortRegistry()
        let original = try registry.reconcile(profile: "JMS", proxies: [node("a"), node("b")],
                                              firstPort: 21000, reserved: [], available: { _ in true })
        _ = try registry.reconcile(profile: "JMS", proxies: [node("b")], firstPort: 21000, reserved: [], available: { _ in true })
        var rotated = node("a")
        rotated["uuid"] = "new-secret"
        let updated = try registry.reconcile(profile: "JMS", proxies: [node("b"), rotated],
                                             firstPort: 23000, reserved: [], available: { _ in true })
        XCTAssertEqual(updated.map { $0.port }, original.reversed().map { $0.port })
    }

    func testUniqueServerRenameKeepsPortButAmbiguousServersDoNotGuess() throws {
        var registry = NodePortRegistry()
        let original = try registry.reconcile(profile: "JMS", proxies: [node("old", server: "s1.example")],
                                              firstPort: 21000, reserved: [], available: { _ in true })
        let renamed = try registry.reconcile(profile: "JMS", proxies: [node("new", server: "s1.example")],
                                             firstPort: 21000, reserved: [], available: { _ in true })
        XCTAssertEqual(original[0].port, renamed[0].port)
        let ambiguous = try registry.reconcile(profile: "JMS", proxies: [node("c", server: "s1.example"), node("d", server: "s1.example")],
                                               firstPort: 21000, reserved: [], available: { _ in true })
        XCTAssertFalse(ambiguous.contains { $0.port == original[0].port })
    }

    func testAllocationsSkipConfiguredAndOccupiedPortsScopedToActiveProfile() throws {
        var registry = NodePortRegistry()
        let a = try registry.reconcile(profile: "one", proxies: [node("a")], firstPort: 21000,
                                       reserved: [21000], available: { $0 != 21001 })
        let b = try registry.reconcile(profile: "two", proxies: [node("a")], firstPort: 21000,
                                       reserved: [21000], available: { $0 != 21001 })
        XCTAssertEqual(a[0].port, 21002)
        XCTAssertEqual(b[0].port, 21002)
        XCTAssertThrowsError(try registry.reconcile(profile: "one", proxies: [node("a")], firstPort: 21000,
                                                   reserved: [21002], available: { _ in true }))
    }

    func testReconcileToleratesDuplicateAndEmptyNamesAndProviderProxies() throws {
        let suite = "ClashFX.NodeServices.Tests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let store = NodeServiceStore(defaults: defaults)
        var root: [String: Any] = [
            "mixed-port": 7890,
            "proxies": [node("alpha"), node(""), node("alpha")],
            "proxy-providers": ["airport": ["type": "http"]]
        ]
        let bindings = try store.prepare(
            root: &root,
            profile: "JMS",
            providerProxies: [("airport", node("beta-from-provider"))],
            available: { _ in true }
        )
        XCTAssertEqual(bindings.map { $0.name }, ["alpha", "beta-from-provider"])
        let groups = root["proxy-groups"] as? [[String: Any]] ?? []
        XCTAssertTrue(groups.contains { ($0["name"] as? String) == "clashfx-node-service-target-\(bindings[1].port)" })
        var saved = try store.read()
        XCTAssertThrowsError(try saved.registry.setPorts(["alpha": 7890], profile: "JMS", reserved: [7890]))
    }

    func testPortEditsAreAtomicAndAllowSwaps() throws {
        var registry = NodePortRegistry()
        _ = try registry.reconcile(profile: "JMS", proxies: [node("a"), node("b")], firstPort: 21000,
                                   reserved: [], available: { _ in true })
        try registry.setPorts(["a": 21001, "b": 21000], profile: "JMS")
        XCTAssertEqual(registry.profiles["JMS"]?.map { $0.port }, [21001, 21000])
        let original = registry
        XCTAssertThrowsError(try registry.setPorts(["a": 21000], profile: "JMS"))
        XCTAssertEqual(registry, original)
        XCTAssertThrowsError(try registry.setPort(80, profile: "JMS", node: "a"))
    }

    func testPinnedListenersKeepUserListenersAndNeverExposeLAN() {
        var root: [String: Any] = ["listeners": [["name": "user", "type": "mixed", "port": 22000]], "rules": ["MATCH,DIRECT"]]
        let bindings = [NodePortBinding(name: "a", serverKey: "a", port: 21000)]
        NodeListenerPolicy.apply(to: &root, bindings: bindings, directPort: 20999)
        NodeListenerPolicy.apply(to: &root, bindings: bindings, directPort: 20999)
        let listeners = root["listeners"] as! [[String: Any]]
        XCTAssertEqual(listeners.count, 3)
        XCTAssertEqual(listeners[0]["name"] as? String, "user")
        XCTAssertTrue(listeners.dropFirst().allSatisfy { $0["listen"] as? String == "127.0.0.1" })
        XCTAssertEqual(listeners[1]["proxy"] as? String, "a")
        XCTAssertEqual(listeners[2]["proxy"] as? String, "DIRECT")
        XCTAssertEqual(root["rules"] as? [String], ["MATCH,DIRECT"])
        NodeListenerPolicy.apply(to: &root, bindings: [], directPort: nil)
        let paused = root["listeners"] as! [[String: Any]]
        XCTAssertEqual(paused.count, 1)
        XCTAssertEqual(paused[0]["name"] as? String, "user")
    }

    func testListenerPortRangesAreReserved() {
        let ports = NodeListenerPolicy.reservedPorts(in: ["mixed-port": 7890, "external-controller": "127.0.0.1:9090",
                                                         "listeners": [["name": "user", "port": "21000-21002/22000"]]])
        XCTAssertTrue(Set([7890, 9090, 21000, 21001, 21002, 22000]).isSubset(of: ports))
    }

    func testOnlyConsecutiveConfirmedNodeFailuresTriggerRecovery() {
        var policy = NodeHealthRecoveryPolicy()
        XCTAssertTrue(policy.record(["a": .failed], threshold: 3).isEmpty)
        XCTAssertTrue(policy.record(["a": .failed], threshold: 3).isEmpty)
        XCTAssertTrue(policy.record(["a": .unavailable], threshold: 3).isEmpty)
        XCTAssertTrue(policy.record(["a": .failed], threshold: 3).isEmpty)
        XCTAssertTrue(policy.record(["a": .healthy(123)], threshold: 3).isEmpty)
        for _ in 0 ..< 2 { XCTAssertTrue(policy.record(["a": .failed, "b": .healthy(12)], threshold: 3).isEmpty) }
        XCTAssertEqual(policy.record(["a": .failed, "b": .healthy(13)], threshold: 3), ["a"])
        XCTAssertTrue(policy.record(["b": .healthy(13)], threshold: 3).isEmpty)
        XCTAssertNil(policy.failures["a"])
    }

    func testCooldownIncludesFailedRefreshAttemptsAndClockRollback() {
        let last = Date(timeIntervalSince1970: 1000)
        XCTAssertFalse(NodeHealthRecoveryPolicy.canRefresh(now: last.addingTimeInterval(299), lastAttempt: last, cooldown: 300))
        XCTAssertTrue(NodeHealthRecoveryPolicy.canRefresh(now: last.addingTimeInterval(300), lastAttempt: last, cooldown: 300))
        XCTAssertFalse(NodeHealthRecoveryPolicy.canRefresh(now: last.addingTimeInterval(-1), lastAttempt: last, cooldown: 300))
        XCTAssertTrue(NodeHealthRecoveryPolicy.canRefresh(now: last, lastAttempt: nil, cooldown: 300))
    }

    func testStateSurvivesStoreRecreationAndCorruptionIsNotOverwritten() throws {
        let suite = "ClashFX.NodeServices.Tests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let store = NodeServiceStore(defaults: defaults)
        var root: [String: Any] = ["proxies": [node("a")]]
        let first = try store.prepare(root: &root, profile: "JMS", available: { _ in true })
        var saved = try store.read()
        saved.lastRefresh["JMS"] = Date(timeIntervalSince1970: 1000)
        try store.write(saved)
        let reopened = NodeServiceStore(defaults: defaults)
        XCTAssertEqual(try reopened.read().registry.profiles["JMS"], first)
        XCTAssertEqual(try reopened.read().lastRefresh["JMS"], saved.lastRefresh["JMS"])
        defaults.set(Data("broken".utf8), forKey: NodeServiceStore.key)
        XCTAssertThrowsError(try reopened.prepare(root: &root, profile: "JMS", available: { _ in true }))
        XCTAssertEqual(defaults.data(forKey: NodeServiceStore.key), Data("broken".utf8))
    }

    func testDisabledNodePortsCanRecoverFromPortConflict() throws {
        let suite = "ClashFX.NodeServices.Tests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        let store = NodeServiceStore(defaults: defaults)
        var state = NodeServiceState()
        state.settings.portsEnabled = false
        state.registry.profiles["JMS"] = [NodePortBinding(name: "a", serverKey: "a", port: 7890)]
        try store.write(state)
        var root: [String: Any] = ["mixed-port": 7890, "proxies": [node("a")]]
        _ = try store.prepare(root: &root, profile: "JMS", available: { _ in true })
        let listeners = root["listeners"] as! [[String: Any]]
        XCTAssertEqual(listeners.count, 1)
        XCTAssertEqual(listeners[0]["proxy"] as? String, "DIRECT")
    }

    func testDirectTransportExplicitlyPinsBothSchemesAndDisablesPACAndSOCKS() {
        let settings = NodeDirectTransportPolicy.proxySettings(port: 20999)
        XCTAssertEqual(settings["HTTPPort"] as? Int, 20999)
        XCTAssertEqual(settings["HTTPSPort"] as? Int, 20999)
        XCTAssertEqual(settings["HTTPProxy"] as? String, "127.0.0.1")
        for key in ["SOCKSEnable", "ProxyAutoConfigEnable", "ProxyAutoDiscoveryEnable"] {
            XCTAssertEqual(settings[key] as? Int, 0)
        }
    }

    func testLegacyMappingsDefaultToEnabledAndIndividualStopsKeepTheirPort() throws {
        let old = Data(#"{"name":"Alpha","serverKey":"http|server","port":21000}"#.utf8)
        let migrated = try JSONDecoder().decode(NodePortBinding.self, from: old)
        XCTAssertTrue(migrated.enabled)
        var registry = NodePortRegistry()
        let before = try registry.reconcile(profile: "JMS", proxies: [node("a"), node("b")], firstPort: 21000,
                                            reserved: [], available: { _ in true })
        try registry.setEnabled(["b": false], profile: "JMS")
        let after = try registry.reconcile(profile: "JMS", proxies: [node("b"), node("a")], firstPort: 23000,
                                           reserved: [], available: { _ in true })
        XCTAssertEqual(after.first?.port, before.last?.port)
        XCTAssertFalse(after.first!.enabled)
        var root = [String: Any]()
        NodeListenerPolicy.apply(to: &root, bindings: after, directPort: nil)
        XCTAssertEqual((root["listeners"] as? [[String: Any]])?.count, 1)
        try registry.setEnabled(["b": true], profile: "JMS")
        XCTAssertEqual(registry.profiles["JMS"]?.first(where: { $0.name == "b" })?.port, before.last?.port)
    }

    func testMultiNodeRulesPreserveAILockAndCleanUpOnDisable() {
        var root: [String: Any] = ["sub-rules": ["user": ["MATCH,DIRECT"]]]
        let binding = NodePortBinding(name: "Alpha", serverKey: "a", port: 21000)
        let protection = ["DOMAIN-SUFFIX,openai.com,Locked", "DOMAIN-SUFFIX,openai.com,REJECT"]
        NodeListenerPolicy.apply(to: &root, bindings: [binding], directPort: 20999, protectionRules: protection)
        let listeners = root["listeners"] as! [[String: Any]]
        let rules = root["sub-rules"] as! [String: Any]
        for entry in listeners {
            XCTAssertNil(entry["proxy"])
            let name = entry["rule"] as! String
            XCTAssertEqual(Array((rules[name] as! [String]).prefix(2)), protection)
        }
        let nodeRules = rules[listeners[0]["rule"] as! String] as! [String]
        XCTAssertEqual(Array(nodeRules.suffix(2)), ["MATCH,Alpha", "MATCH,REJECT"])
        let directRules = rules[listeners[1]["rule"] as! String] as! [String]
        XCTAssertEqual(Array(directRules.suffix(2)), ["MATCH,DIRECT", "MATCH,REJECT"])
        NodeListenerPolicy.apply(to: &root, bindings: [binding], directPort: nil)
        XCTAssertEqual((root["sub-rules"] as! [String: Any]).count, 1)
        XCTAssertEqual((root["listeners"] as! [[String: Any]])[0]["proxy"] as? String, "Alpha")
    }
}

final class NodeDownloadSpeedTests: XCTestCase {
    private final class Probe: NodeDownloadSpeedOperation {
        var started = false
        var cancelled = false
        var progress: ((Double) -> Void)?
        var completion: ((NodeDownloadSpeedSample) -> Void)?
        func start(progress: @escaping (Double) -> Void, completion: @escaping (NodeDownloadSpeedSample) -> Void) {
            started = true
            self.progress = progress
            self.completion = completion
        }
        func cancel() { cancelled = true; completion?(.cancelled) }
    }

    func testOnlySavedEnabledActivePortsAreSelected() {
        var state = NodeServiceState()
        state.activeNames["local:JMS"] = ["Alpha", "Beta", "Gamma"]
        state.registry.profiles["local:JMS"] = [
            NodePortBinding(name: "Alpha", serverKey: "a", port: 21002),
            NodePortBinding(name: "Beta", serverKey: "b", port: 21001, enabled: false),
            NodePortBinding(name: "Gamma", serverKey: "c", port: 21000),
            NodePortBinding(name: "Retired", serverKey: "d", port: 21003)
        ]
        XCTAssertEqual(NodeDownloadSpeedPolicy.targets(state: state, profile: "local:JMS"), [
            NodeDownloadSpeedTarget(name: "Gamma", port: 21000), NodeDownloadSpeedTarget(name: "Alpha", port: 21002)
        ])
        state.settings.portsEnabled = false
        XCTAssertTrue(NodeDownloadSpeedPolicy.targets(state: state, profile: "local:JMS").isEmpty)
    }

    func testRatesUseDecimalMegabytesAndRejectInvalidSamples() throws {
        XCTAssertEqual(try XCTUnwrap(NodeDownloadSpeedPolicy.megabytesPerSecond(bytes: 20_000_000, elapsed: 2)), 10, accuracy: 0.001)
        XCTAssertNil(NodeDownloadSpeedPolicy.megabytesPerSecond(bytes: 0, elapsed: 2))
        XCTAssertNil(NodeDownloadSpeedPolicy.megabytesPerSecond(bytes: 2, elapsed: 0))
        XCTAssertNil(NodeDownloadSpeedPolicy.megabytesPerSecond(bytes: 2, elapsed: .infinity))
    }

    func testProbePinsBothHTTPAndHTTPSWithoutSystemProxyDiscovery() {
        let settings = NodeDownloadSpeedPolicy.proxySettings(port: 21005)
        XCTAssertEqual(settings["HTTPPort"] as? Int, 21005)
        XCTAssertEqual(settings["HTTPSPort"] as? Int, 21005)
        XCTAssertEqual(settings["HTTPProxy"] as? String, "127.0.0.1")
        XCTAssertEqual(settings["HTTPSProxy"] as? String, "127.0.0.1")
        XCTAssertEqual(settings["ProxyAutoConfigEnable"] as? Int, 0)
        XCTAssertEqual(settings["ProxyAutoDiscoveryEnable"] as? Int, 0)
        XCTAssertEqual(settings["ExceptionsList"] as? [String], [])
    }

    func testDownloadsRunSeriallyAndPublishEachResult() {
        var probes = [Probe]()
        var names = [String]()
        let runner = NodeDownloadSpeedRunner { target in
            names.append(target.name)
            let probe = Probe(); probes.append(probe); return probe
        }
        runner.start(targets: [.init(name: "Alpha", port: 21000), .init(name: "Beta", port: 21001)], isCurrent: { true })
        XCTAssertEqual(names, ["Alpha"])
        XCTAssertEqual(runner.samples["Beta"], .waiting)
        probes[0].progress?(2)
        XCTAssertEqual(runner.samples["Alpha"], .measuring(2))
        probes[0].completion?(.measured(3))
        XCTAssertEqual(names, ["Alpha", "Beta"])
        XCTAssertEqual(runner.samples["Alpha"], .measured(3))
        probes[1].completion?(.failed)
        XCTAssertFalse(runner.isRunning)
        XCTAssertEqual(runner.samples["Beta"], .failed)
    }

    func testStopFencesLateCallbacksAndDoesNotStartQueuedNodes() {
        let probe = Probe()
        var made = 0
        let runner = NodeDownloadSpeedRunner { _ in made += 1; return probe }
        runner.start(targets: [.init(name: "Alpha", port: 21000), .init(name: "Beta", port: 21001)], isCurrent: { true })
        runner.cancel()
        probe.progress?(99)
        probe.completion?(.measured(99))
        XCTAssertTrue(probe.cancelled)
        XCTAssertFalse(runner.isRunning)
        XCTAssertEqual(made, 1)
        XCTAssertEqual(runner.samples["Alpha"], .cancelled)
        XCTAssertEqual(runner.samples["Beta"], .cancelled)
    }

    func testScopeChangeCancelsAndDiscardsStaleResults() {
        let probe = Probe()
        var current = true
        let runner = NodeDownloadSpeedRunner { _ in probe }
        runner.start(targets: [.init(name: "Alpha", port: 21000)], isCurrent: { current })
        current = false
        probe.progress?(5)
        probe.completion?(.measured(9))
        XCTAssertTrue(probe.cancelled)
        XCTAssertFalse(runner.isRunning)
        XCTAssertTrue(runner.samples.isEmpty)
    }

    func testEmptySelectionNeverCreatesNetworkRequest() {
        var made = 0
        let runner = NodeDownloadSpeedRunner { _ in made += 1; return Probe() }
        runner.start(targets: [], isCurrent: { true })
        XCTAssertFalse(runner.isRunning)
        XCTAssertEqual(made, 0)
    }
}
