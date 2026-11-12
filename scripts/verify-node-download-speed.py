#!/usr/bin/env python3
"""Exercise the production streaming download probe with loopback HTTP proxies.

No real subscription, AI client, system proxy, or external download is used.
"""
from pathlib import Path
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import socket
import subprocess
import tempfile
import threading
import time
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[1]
SWIFT = r'''
import Foundation
let port = Int(CommandLine.arguments[1])!
let url = URL(string: CommandLine.arguments[2])!
let duration = Double(CommandLine.arguments[3])!
let limit = Int(CommandLine.arguments[4])!
let cancelAfter = Double(CommandLine.arguments[5])!
let probe = NodeDownloadSpeedProbe(port: port, url: url, maximumBytes: limit, duration: duration)
var result: NodeDownloadSpeedSample?
var completions = 0
var progress = 0
probe.start(progress: { _ in progress += 1 }, completion: { value in completions += 1; result = value })
if cancelAfter >= 0 { DispatchQueue.main.asyncAfter(deadline: .now() + cancelAfter) { probe.cancel() } }
let deadline = Date().addingTimeInterval(duration + 3)
while result == nil && Date() < deadline { RunLoop.current.run(until: Date().addingTimeInterval(0.005)) }
RunLoop.current.run(until: Date().addingTimeInterval(0.1))
guard let result, completions == 1 else { fatalError("missing or duplicate probe completion") }
var output: [String: Any] = ["completions": completions, "progress": progress]
switch result {
case let .measured(speed): output["result"] = "measured"; output["mbps"] = speed
case .failed: output["result"] = "failed"
case .cancelled: output["result"] = "cancelled"
default: fatalError("unexpected terminal probe state")
}
let data = try JSONSerialization.data(withJSONObject: output, options: [.sortedKeys])
print(String(data: data, encoding: .utf8)!)
'''


class Proxy(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def handle(self):
        try:
            super().handle()
        except (BrokenPipeError, ConnectionResetError):
            pass  # Expected when a bounded or cancelled probe closes its socket.

    def log_message(self, *_):
        pass

    def do_GET(self):
        self.server.requests.append(self.path)
        path = urlsplit(self.path).path
        if path == '/redirect':
            self.send_response(302)
            self.send_header('Location', 'http://redirect-target.fixture.invalid/payload')
            self.send_header('Content-Length', '0')
            self.end_headers()
            return
        if path == '/failure':
            self.send_response(502)
            self.send_header('Content-Length', '0')
            self.end_headers()
            return
        if path == '/html':
            self.send_response(200)
            self.send_header('Content-Type', 'text/html')
            self.send_header('Content-Length', '64')
            self.end_headers()
            try:
                self.wfile.write(b'x' * 64)
            except OSError:
                pass
            return
        size = 8_000_000 if path in ['/window', '/cancel', '/disconnect'] else 2_000_000
        self.send_response(200)
        self.send_header('Content-Type', 'application/octet-stream')
        self.send_header('Content-Length', str(size))
        self.send_header('Cache-Control', 'no-store')
        self.end_headers()
        sent = 0
        try:
            while sent < size:
                block = b'x' * min(16384, size - sent)
                self.wfile.write(block)
                self.wfile.flush()
                sent += len(block)
                if path == '/disconnect' and sent >= 65536:
                    self.close_connection = True
                    return
                time.sleep(self.server.pause)
        except (BrokenPipeError, ConnectionResetError):
            pass


def server(pause):
    instance = ThreadingHTTPServer(('127.0.0.1', 0), Proxy)
    instance.daemon_threads = True
    instance.pause = pause
    instance.requests = []
    threading.Thread(target=instance.serve_forever, daemon=True).start()
    return instance


def main():
    alpha, beta = server(0.002), server(0.015)
    checks = []
    try:
        with tempfile.TemporaryDirectory(prefix='clashfx-download-acceptance-') as temporary:
            temporary = Path(temporary)
            (temporary / 'main.swift').write_text(SWIFT)
            executable = temporary / 'download-probe'
            compile_result = subprocess.run([
                'swiftc', '-swift-version', '5', '-module-cache-path', str(temporary / 'modules'),
                str(ROOT / 'ClashFX/General/Utils/NodeServicePolicy.swift'),
                str(ROOT / 'ClashFX/General/Managers/NodeDownloadSpeed.swift'),
                str(temporary / 'main.swift'), '-o', str(executable)
            ], capture_output=True, text=True)
            if compile_result.returncode:
                raise SystemExit(compile_result.stderr)

            def probe(proxy, path='/payload', duration=4, limit=20_000_000, cancel=-1):
                port = proxy.server_port if hasattr(proxy, 'server_port') else proxy
                output = subprocess.run([str(executable), str(port), 'http://download.fixture.invalid' + path,
                                         str(duration), str(limit), str(cancel)],
                                        capture_output=True, text=True, check=True, timeout=duration + 5)
                return json.loads(output.stdout)

            fast = probe(alpha)
            slow = probe(beta)
            assert fast['result'] == slow['result'] == 'measured', (fast, slow)
            assert fast['mbps'] > slow['mbps'] * 2 > 0, (fast, slow)
            assert len(alpha.requests) == len(beta.requests) == 1
            checks.append('each fixed proxy port measures its own streamed download, in decimal MB/s')

            started = time.monotonic()
            capped = probe(alpha, '/window', duration=3, limit=200_000)
            assert capped['result'] == 'measured' and time.monotonic() - started < 1.5, capped
            checks.append('byte limit ends the transfer before the full fixture payload')

            partial = probe(beta, '/window', duration=.35)
            assert partial['result'] == 'measured' and partial['mbps'] > 0, partial
            checks.append('time limit reports the measured partial transfer and releases the connection')

            cancelled = probe(beta, '/cancel', cancel=.15)
            assert cancelled['result'] == 'cancelled' and cancelled['completions'] == 1, cancelled
            checks.append('manual stop terminates streaming with exactly one completion')

            for path in ['/failure', '/html', '/disconnect']:
                failed = probe(alpha, path)
                assert failed['result'] == 'failed', (path, failed)
            checks.append('proxy errors, captive portal HTML and interrupted payloads are not reported as valid speeds')

            count = len(alpha.requests)
            assert probe(alpha, '/redirect')['result'] == 'failed'
            assert len(alpha.requests) == count + 1
            checks.append('redirect responses are rejected without downloading another destination')

            with socket.socket() as reserved:
                reserved.bind(('127.0.0.1', 0))
                unavailable = reserved.getsockname()[1]
                assert probe(unavailable, duration=.5)['result'] == 'failed'
            checks.append('unavailable fixed proxy port fails without a successful fallback')
    finally:
        for instance in [alpha, beta]:
            instance.shutdown()
            instance.server_close()
    print(json.dumps({'passed': True, 'checks': checks}, indent=2))


if __name__ == '__main__':
    main()
