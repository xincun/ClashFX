#!/usr/bin/env python3
"""Loopback-only acceptance for persistent node ports and DIRECT recovery.

Builds the production Swift policy, runs a disposable no-TUN core, and uses
local HTTP proxy/origin fixtures. Never opens real subscriptions or controls
the installed app, helper, system proxy, DNS, or routes.
"""
from __future__ import annotations

import argparse
import http.client
import http.server
import json
from pathlib import Path
import socket
import struct
import subprocess
import tempfile
import threading
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
SWIFT = r'''
import Darwin
import Foundation

func free(_ port: Int) -> Bool {
    for kind in [SOCK_STREAM, SOCK_DGRAM] {
        let fd = socket(AF_INET, kind, 0)
        guard fd >= 0 else { return false }
        defer { close(fd) }
        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = in_port_t(port).bigEndian
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        if withUnsafePointer(to: &addr, { p in
            p.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(fd, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        }) != 0 { return false }
    }
    return true
}

let action = CommandLine.arguments[1]
if action == "generate" {
    let directory = URL(fileURLWithPath: CommandLine.arguments[2])
    var root = try JSONSerialization.jsonObject(with: Data(contentsOf: directory.appendingPathComponent("template.json"))) as! [String: Any]
    let stateURL = directory.appendingPathComponent("state.json")
    let suite = "ClashFX.NodeAcceptance.\(UUID().uuidString)"
    let defaults = UserDefaults(suiteName: suite)!
    defer { defaults.removePersistentDomain(forName: suite) }
    let store = NodeServiceStore(defaults: defaults)
    var state = FileManager.default.fileExists(atPath: stateURL.path)
        ? try JSONDecoder().decode(NodeServiceState.self, from: Data(contentsOf: stateURL)) : NodeServiceState()
    state.settings.firstPort = 41000
    if CommandLine.arguments.count > 3 {
        try state.registry.setPorts(["Alpha": Int(CommandLine.arguments[3])!], profile: "fixture")
    }
    try store.write(state)
    let bindings = try store.prepare(root: &root, profile: "fixture", available: free)
    try JSONSerialization.data(withJSONObject: root, options: [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]).write(to: directory.appendingPathComponent("runtime.json"))
    let updated = try store.read()
    try JSONEncoder().encode(updated).write(to: stateURL)
    print(String(data: try JSONSerialization.data(withJSONObject: ["ports": Dictionary(uniqueKeysWithValues: bindings.map { ($0.name, $0.port) }), "direct": updated.registry.directPort!]), encoding: .utf8)!)
} else if action == "fetch" {
    let configuration = URLSessionConfiguration.ephemeral
    configuration.connectionProxyDictionary = NodeDirectTransportPolicy.proxySettings(port: Int(CommandLine.arguments[3])!)
    configuration.timeoutIntervalForRequest = 3
    configuration.timeoutIntervalForResource = 5
    configuration.requestCachePolicy = .reloadIgnoringLocalAndRemoteCacheData
    let session = URLSession(configuration: configuration)
    var finished = false
    var success = false
    let task = session.dataTask(with: URL(string: CommandLine.arguments[2])!) { data, response, error in
        success = error == nil && (response as? HTTPURLResponse)?.statusCode == 200 && String(data: data ?? Data(), encoding: .utf8) == "direct subscription"
        finished = true
    }
    task.resume()
    let deadline = Date().addingTimeInterval(7)
    while !finished && Date() < deadline { RunLoop.current.run(until: Date().addingTimeInterval(0.01)) }
    session.invalidateAndCancel()
    print(success ? "DIRECT_FETCH_OK" : "DIRECT_FETCH_FAILED")
    exit(success ? 0 : 1)
}
'''


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_):
        pass

    def do_CONNECT(self):
        self.send_response(200)
        self.end_headers()
        self.close_connection = False

    def do_GET(self):
        self.server.requests.append(self.path)
        if self.path == "/redirect":
            self.send_response(302)
            self.send_header("Location", self.server.redirect)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        body = self.server.marker.encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def server(marker):
    instance = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    instance.daemon_threads = True
    instance.marker = marker
    instance.requests = []
    threading.Thread(target=instance.serve_forever, daemon=True).start()
    return instance


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def request(port, url):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=4)
    try:
        connection.request("GET", url)
        response = connection.getresponse()
        return response.status, response.read().decode()
    finally:
        connection.close()


def exact(sock, count):
    data = b""
    while len(data) < count:
        block = sock.recv(count - len(data))
        assert block, "unexpected SOCKS EOF"
        data += block
    return data


def socks_request(port):
    with socket.create_connection(("127.0.0.1", port), timeout=4) as sock:
        sock.sendall(b"\x05\x01\x00")
        assert exact(sock, 2) == b"\x05\x00"
        host = b"fixture.invalid"
        sock.sendall(b"\x05\x01\x00\x03" + bytes([len(host)]) + host + struct.pack("!H", 80))
        header = exact(sock, 4)
        assert header[:2] == b"\x05\x00", "SOCKS connect failed"
        size = {1: 4, 4: 16}.get(header[3])
        if header[3] == 3:
            size = exact(sock, 1)[0]
        exact(sock, size + 2)
        sock.sendall(b"GET / HTTP/1.1\r\nHost: fixture.invalid\r\nConnection: close\r\n\r\n")
        response = http.client.HTTPResponse(sock)
        response.begin()
        return response.status, response.read().decode()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--core", type=Path, default=ROOT / "ClashFX/goClash/mihomo_core")
    args = parser.parse_args()
    fixtures = [server("Alpha"), server("Beta"), server("direct subscription"), server("direct subscription")]
    alpha, beta, origin, redirected = fixtures
    origin.redirect = f"http://redirect.invalid:{redirected.server_port}/subscription"
    checks = []
    core = None
    try:
        with tempfile.TemporaryDirectory(prefix="clashfx-node-acceptance-") as temporary:
            directory = Path(temporary)
            (directory / "main.swift").write_text(SWIFT)
            executable = directory / "policy-fixture"
            subprocess.run(["swiftc", "-swift-version", "5", "-module-cache-path", str(directory / "modules"),
                            str(ROOT / "ClashFX/General/Utils/NodeServicePolicy.swift"), str(directory / "main.swift"),
                            "-o", str(executable)], check=True, capture_output=True, text=True)
            api, mixed = free_port(), free_port()
            template = {
                "mode": "rule", "log-level": "warning", "mixed-port": mixed,
                "allow-lan": False, "external-controller": f"127.0.0.1:{api}", "tun": {"enable": False},
                "hosts": {"subscription.invalid": "127.0.0.1", "redirect.invalid": "127.0.0.1"},
                "proxies": [{"name": name, "type": "http", "server": "127.0.0.1", "port": fixture.server_port}
                            for name, fixture in [("Alpha", alpha), ("Beta", beta)]],
                "rules": ["MATCH,REJECT"],
            }

            def generate(rebind=None):
                (directory / "template.json").write_text(json.dumps(template))
                command = [str(executable), "generate", str(directory)]
                if rebind is not None:
                    command.append(str(rebind))
                return json.loads(subprocess.check_output(command, text=True))

            plan = generate()
            initial = plan["ports"]
            with (directory / "core.log").open("wb") as log:
                core = subprocess.Popen([str(args.core.resolve()), "-d", str(directory), "-f", str(directory / "runtime.json")],
                                        stdout=log, stderr=subprocess.STDOUT)
                opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

                def call(path, body=None, method=None):
                    data = None if body is None else json.dumps(body).encode()
                    req = urllib.request.Request(f"http://127.0.0.1:{api}{path}", data=data, method=method,
                                                 headers={"Content-Type": "application/json"})
                    with opener.open(req, timeout=5) as response:
                        return response.read()

                for attempt in range(60):
                    assert core.poll() is None, (directory / "core.log").read_text()
                    try:
                        call("/version")
                        break
                    except OSError:
                        time.sleep(0.1)
                else:
                    raise AssertionError("isolated core did not become ready")

                for name, port in initial.items():
                    assert request(port, "http://fixture.invalid/") == (200, name)
                    assert socks_request(port) == (200, name)
                checks.append("HTTP and SOCKS5 stay pinned to each node despite MATCH,REJECT")

                template["proxies"].reverse()
                assert generate()["ports"] == initial
                call("/configs", {"path": str(directory / "runtime.json")}, "PUT")
                for name, port in initial.items():
                    assert request(port, "http://fixture.invalid/after-refresh") == (200, name)
                checks.append("subscription reordering and real core reload retain fixed ports")

                replacement = free_port()
                changed = generate(replacement)
                call("/configs", {"path": str(directory / "runtime.json")}, "PUT")
                assert request(changed["ports"]["Alpha"], "http://fixture.invalid/rebound") == (200, "Alpha")
                assert request(changed["ports"]["Beta"], "http://fixture.invalid/rebound") == (200, "Beta")
                assert changed["ports"]["Beta"] == initial["Beta"]
                checks.append("manual port changes apply without moving other nodes")

                call("/configs", {"mode": "global"}, "PATCH")
                call("/proxies/GLOBAL", {"name": "REJECT"}, "PUT")
                before = len(alpha.requests) + len(beta.requests)
                url = f"http://subscription.invalid:{origin.server_port}/redirect"
                fetched = subprocess.run([str(executable), "fetch", url, str(plan["direct"])], capture_output=True, text=True)
                assert fetched.returncode == 0, fetched.stdout + fetched.stderr
                assert "/redirect" in origin.requests and "/subscription" in redirected.requests
                assert len(alpha.requests) + len(beta.requests) == before
                checks.append("Foundation subscription fetch and cross-host redirect use DIRECT even with GLOBAL=REJECT")

                core.terminate()
                core.wait(timeout=8)
                hits = len(origin.requests) + len(redirected.requests)
                failed = subprocess.run([str(executable), "fetch", url, str(plan["direct"])], capture_output=True, text=True)
                assert failed.returncode != 0
                assert len(origin.requests) + len(redirected.requests) == hits
                assert len(alpha.requests) + len(beta.requests) == before
                checks.append("unavailable DIRECT listener fails closed without fallback to another proxy")
        print(json.dumps({"passed": True, "checks": checks}, ensure_ascii=False, indent=2))
    finally:
        if core is not None and core.poll() is None:
            core.terminate()
            core.wait(timeout=8)
        for fixture in fixtures:
            fixture.shutdown()
            fixture.server_close()


if __name__ == "__main__":
    main()
