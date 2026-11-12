#!/usr/bin/env python3
"""Exercise the production watchdog with isolated defaults and controlled I/O.

The core probes, subscription downloader and app reload callbacks are test
doubles. No app is started and no real network/subscription is accessed.
"""
from pathlib import Path
import json
import subprocess
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[1]
SWIFT = r'''
import Cocoa
extension Notification.Name { static let reloadDashboard = Notification.Name("fixture.reloadDashboard") }

enum ProxyDelayOutcome { case measured(Int), failed, timedOut, unavailable, cancelled }
enum LogLevel { case info, warning }
enum Logger { static func log(_ text: String, level: LogLevel = .info) {} }
enum Settings { static var claudeProxyLockEnabled = false }
final class Flag { var value = false }
final class ICloudManager { static let shared = ICloudManager(); let useiCloud = Flag() }
enum ConfigManager { static var selectConfigName = "fixture" }
final class AppDelegate {
    static let shared = AppDelegate()
    var nodeServiceChecksAllowed = true
    var isTerminating = false
    var reloads = 0
    func updateConfig(showNotification: Bool, showError: Bool, completeHandler: @escaping (String?) -> Void) {
        reloads += 1
        completeHandler(nil)
    }
}
final class RemoteConfigModel {
    let name = "fixture"
    var updating = false
}
final class RemoteConfigManager {
    static let shared = RemoteConfigManager()
    var configs = [RemoteConfigModel()]
    static var calls = [Bool]()
    static var fail = false
    static func updateConfig(config: RemoteConfigModel, forceDirect: Bool, complete: @escaping (String?) -> Void) {
        calls.append(forceDirect)
        complete(fail ? "fixture download failed" : nil)
    }
    func saveConfigs() {}
}
enum ApiRequest {
    struct Context: Equatable { let version: Int }
    final class BenchmarkSession {
        private(set) var isCancelled = false
        func cancel() { isCancelled = true }
    }
    static var scope = 0
    static var requests = 0
    static var outcome: ProxyDelayOutcome = .failed
    static var cancelNext = false
    static var switchNext = false
    static func runtimeConfigContext() -> Context { Context(version: scope) }
    static func getProxyDelayOutcome(proxyName: String, benchmarkURL: String, timeout: Int,
                                    session: BenchmarkSession?, expectedStatus: String?, callback: @escaping (ProxyDelayOutcome) -> Void) {
        requests += 1
        if cancelNext {
            cancelNext = false
            session?.cancel()
            callback(.cancelled)
        } else if switchNext {
            switchNext = false
            ConfigManager.selectConfigName = "other"
            scope += 1
            callback(.failed)
        } else { callback(outcome) }
    }
}

func check(_ condition: @autoclosure () -> Bool, _ message: String) {
    if !condition() { FileHandle.standardError.write(Data((message + "\n").utf8)); exit(1) }
}
let suite = "ClashFX.WatchdogAcceptance.\(UUID().uuidString)"
let defaults = UserDefaults(suiteName: suite)!
defer { defaults.removePersistentDomain(forName: suite) }
let store = NodeServiceStore(defaults: defaults)

func fresh() throws -> NodeServices {
    ConfigManager.selectConfigName = "fixture"
    AppDelegate.shared.reloads = 0
    RemoteConfigManager.calls = []
    RemoteConfigManager.fail = false
    ApiRequest.outcome = .failed
    ApiRequest.cancelNext = false
    ApiRequest.switchNext = false
    var state = NodeServiceState()
    state.activeNames[NodeServices.profileKey()] = ["Alpha"]
    try store.write(state)
    let monitor = NodeServices(store: store)
    monitor.updateConnectivity(isOnline: true)
    return monitor
}

func round(_ monitor: NodeServices) {
    monitor.checkNow()
    let deadline = Date().addingTimeInterval(2)
    while monitor.isChecking && Date() < deadline { RunLoop.current.run(until: Date().addingTimeInterval(0.005)) }
    check(!monitor.isChecking, "watchdog stalled after a completed/cancelled round")
}

var monitor = try fresh()
round(monitor); round(monitor)
check(RemoteConfigManager.calls.isEmpty, "refreshed before threshold")
round(monitor)
check(RemoteConfigManager.calls == [true], "threshold must request exactly one DIRECT refresh")
check(AppDelegate.shared.reloads == 1, "successful refresh must reload configuration")
for _ in 0 ..< 4 { round(monitor) }
check(RemoteConfigManager.calls == [true], "cooldown did not suppress repeated refreshes")

monitor = try fresh()
RemoteConfigManager.fail = true
for _ in 0 ..< 5 { round(monitor) }
check(RemoteConfigManager.calls == [true], "failed refresh must also enter cooldown")
check(AppDelegate.shared.reloads == 0, "failed download must not reload")

monitor = try fresh()
ApiRequest.cancelNext = true
round(monitor)
ApiRequest.outcome = .measured(42)
round(monitor)
check(monitor.samples["Alpha"] == .healthy(42), "cancelled session prevented the next check")
check(RemoteConfigManager.calls.isEmpty, "cancelled session counted as node failure")

monitor = try fresh()
round(monitor); round(monitor)
monitor.updateConnectivity(isOnline: false)
let requests = ApiRequest.requests
round(monitor)
check(ApiRequest.requests == requests, "offline watchdog still probed nodes")
monitor.updateConnectivity(isOnline: true)
round(monitor)
check(RemoteConfigManager.calls.isEmpty, "offline transition did not reset failures")

monitor = try fresh()
ApiRequest.switchNext = true
round(monitor)
check(RemoteConfigManager.calls.isEmpty, "stale profile results refreshed a new profile")
ConfigManager.selectConfigName = "fixture"
ApiRequest.outcome = .measured(10)
round(monitor)
check(monitor.samples["Alpha"] == .healthy(10), "profile switch stalled later checks")

monitor = try fresh()
Settings.claudeProxyLockEnabled = true
let previous = ApiRequest.requests
round(monitor); round(monitor); round(monitor)
check(ApiRequest.requests > previous, "AI lock incorrectly paused node checks")
check(RemoteConfigManager.calls == [true], "AI lock prevented DIRECT subscription refresh")
Settings.claudeProxyLockEnabled = false
final class DownloadProbe: NodeDownloadSpeedOperation {
    var progress: ((Double) -> Void)?
    var done: ((NodeDownloadSpeedSample) -> Void)?
    var cancelled = false
    func start(progress: @escaping (Double) -> Void, completion: @escaping (NodeDownloadSpeedSample) -> Void) {
        self.progress = progress; done = completion
    }
    func cancel() { cancelled = true; done?(.cancelled) }
}
monitor = try fresh()
var selected = try store.read()
selected.activeNames[NodeServices.profileKey()] = ["Alpha", "Beta"]
selected.registry.profiles[NodeServices.profileKey()] = [
    NodePortBinding(name: "Alpha", serverKey: "a", port: 21000),
    NodePortBinding(name: "Beta", serverKey: "b", port: 21001, enabled: false)
]
try store.write(selected)
var startedDownloads = [String]()
let downloadProbe = DownloadProbe()
let downloadRunner = NodeDownloadSpeedRunner { target in
    startedDownloads.append(target.name); return downloadProbe
}
monitor = NodeServices(store: store, downloadRunner: downloadRunner)
monitor.updateConnectivity(isOnline: true)
monitor.testDownloadSpeeds()
check(startedDownloads == ["Alpha"], "download test included stopped node")
check(monitor.isSpeedTesting, "download test did not start")
let oldRequests = ApiRequest.requests
monitor.checkNow()
monitor.refreshSubscription()
check(ApiRequest.requests == oldRequests && RemoteConfigManager.calls.isEmpty,
      "watchdog or subscription refresh competed with speed test")
downloadProbe.progress?(3)
check(monitor.downloadSpeedForNode("Alpha").contains("MB/s"), "download speed not exposed to table")
ApiRequest.scope += 1
downloadProbe.progress?(4)
check(!monitor.isSpeedTesting && downloadProbe.cancelled, "core scope change did not stop transfer")
check(monitor.downloadSpeedForNode("Alpha") == "—", "stale download result remained visible")
print("WATCHDOG_ACCEPTANCE_OK")
'''

with tempfile.TemporaryDirectory(prefix='clashfx-watchdog-') as temporary:
    directory = Path(temporary)
    source = directory / 'main.swift'
    source.write_text(SWIFT)
    executable = directory / ('watchdog-' + uuid.uuid4().hex)
    compiled = subprocess.run(['swiftc', '-swift-version', '5', '-module-cache-path', str(directory / 'modules'),
                               str(ROOT / 'ClashFX/General/Utils/NodeServicePolicy.swift'),
                               str(ROOT / 'ClashFX/General/Managers/NodeDownloadSpeed.swift'),
                               str(ROOT / 'ClashFX/General/Managers/NodeServices.swift'), str(source), '-o', str(executable)],
                              capture_output=True, text=True)
    if compiled.returncode:
        raise SystemExit(compiled.stderr)
    subprocess.run([str(executable)], check=True)
print(json.dumps({'passed': True, 'checks': ['threshold and DIRECT refresh', 'cooldown after success and failure',
                                          'cancelled session resumes', 'offline pause and reset', 'stale profile discarded',
                                          'AI lock coexists with node checks and direct refresh',
                                          'download test uses enabled nodes and pauses competing tasks',
                                          'download test cancels on runtime scope change']}, ensure_ascii=False, indent=2))
