#!/usr/bin/env python3
"""AI lock + concurrent node ports, using only loopback fixture traffic.

Does not launch Claude, Codex, Antigravity, or their CLI binaries. Compiles
the production policies and exercises a disposable no-TUN mihomo core.
"""
from pathlib import Path
import importlib.util
import json
import socket
import struct
import subprocess
import tempfile
import time
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location('node_fixtures', ROOT / 'scripts/verify-node-services.py')
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)
old = 'let bindings = try store.prepare(root: &root, profile: "fixture", available: free)'
new = '''let target = root.removeValue(forKey: "fixture-ai-target") as? String
    let protection = target.map { ClaudeProxyLockPolicy.rules(target: $0) } ?? []
    let bindings = try store.prepare(root: &root, profile: "fixture", protectionRules: protection, available: free)
    if let target {
        let outcome = ClaudeProxyLockPolicy.apply(to: &root, target: target)
        precondition(outcome.applied)
    }'''
assert old in fixture.SWIFT
SWIFT = fixture.SWIFT.replace(old, new)


def is_open(port):
    try:
        with socket.create_connection(('127.0.0.1', port), timeout=0.3):
            return True
    except OSError:
        return False


def send_udp(proxy, host, port, payload):
    with socket.create_connection(('127.0.0.1', proxy), timeout=3) as tcp:
        tcp.sendall(b'\x05\x01\x00')
        assert fixture.exact(tcp, 2) == b'\x05\x00'
        tcp.sendall(b'\x05\x03\x00\x01\x00\x00\x00\x00\x00\x00')
        header = fixture.exact(tcp, 4)
        assert header[:2] == b'\x05\x00', 'SOCKS UDP associate failed'
        if header[3] == 1:
            address = socket.inet_ntoa(fixture.exact(tcp, 4))
        elif header[3] == 4:
            address = socket.inet_ntop(socket.AF_INET6, fixture.exact(tcp, 16))
        else:
            address = fixture.exact(tcp, fixture.exact(tcp, 1)[0]).decode()
        relay_port = struct.unpack('!H', fixture.exact(tcp, 2))[0]
        if address == '0.0.0.0':
            address = '127.0.0.1'
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as udp:
            udp.bind(('127.0.0.1', 0))
            encoded = host.encode()
            packet = b'\x00\x00\x00\x03' + bytes([len(encoded)]) + encoded + struct.pack('!H', port) + payload
            udp.sendto(packet, (address, relay_port))
            time.sleep(0.1)


def main():
    servers = [fixture.server(name) for name in ['Alpha', 'Beta', 'Locked', 'direct subscription']]
    alpha, beta, locked, origin = servers
    core = None
    checks = []
    try:
        with tempfile.TemporaryDirectory(prefix='clashfx-ai-multi-') as temporary:
            directory = Path(temporary)
            (directory / 'main.swift').write_text(SWIFT)
            executable = directory / 'policy-fixture'
            compiled = subprocess.run(['swiftc', '-swift-version', '5', '-module-cache-path', str(directory / 'modules'),
                                       str(ROOT / 'ClashFX/General/Utils/NodeServicePolicy.swift'),
                                       str(ROOT / 'ClashFX/General/Utils/ClaudeProxyLockPolicy.swift'),
                                       str(directory / 'main.swift'), '-o', str(executable)], capture_output=True, text=True)
            if compiled.returncode:
                raise SystemExit(compiled.stderr)
            api, mixed, control = fixture.free_port(), fixture.free_port(), fixture.free_port()
            hosts = ['api.anthropic.com', 'chatgpt.com', 'api.openai.com', 'antigravity.google',
                     'cloudcode-pa.googleapis.com', 'businessaicode.googleapis.com', 'generativelanguage.googleapis.com',
                     'github.com', 'maps.google.com', 'storage.googleapis.com', 'subscription.invalid', 'udp-origin.invalid']
            template = {
                'mode': 'rule', 'log-level': 'warning', 'mixed-port': mixed, 'allow-lan': False,
                'external-controller': f'127.0.0.1:{api}', 'tun': {'enable': False},
                'hosts': {host: '127.0.0.1' for host in hosts},
                'listeners': [{'name': 'fixture-udp-control', 'type': 'mixed', 'listen': '127.0.0.1',
                               'port': control, 'udp': True, 'proxy': 'DIRECT'}],
                'proxies': [{'name': name, 'type': 'http', 'server': '127.0.0.1', 'port': server.server_port}
                            for name, server in [('Alpha', alpha), ('Beta', beta), ('Locked', locked)]],
                'rules': ['MATCH,REJECT'], 'fixture-ai-target': 'Locked',
            }

            def generate():
                (directory / 'template.json').write_text(json.dumps(template))
                return json.loads(subprocess.check_output([str(executable), 'generate', str(directory)], text=True))

            plan = generate()
            with (directory / 'core.log').open('wb') as log:
                core = subprocess.Popen([str(ROOT / 'ClashFX/goClash/mihomo_core'), '-d', str(directory),
                                         '-f', str(directory / 'runtime.json')], stdout=log, stderr=subprocess.STDOUT)
                opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

                def call(path, body=None, method=None):
                    req = urllib.request.Request(f'http://127.0.0.1:{api}{path}',
                                                 data=None if body is None else json.dumps(body).encode(), method=method,
                                                 headers={'Content-Type': 'application/json'})
                    with opener.open(req, timeout=5) as response:
                        return response.read()

                for _ in range(60):
                    assert core.poll() is None, (directory / 'core.log').read_text()
                    try:
                        call('/version')
                        break
                    except OSError:
                        time.sleep(0.1)
                else:
                    raise AssertionError('fixture core not ready')
                for name in ['Alpha', 'Beta']:
                    for host in ['github.com', 'maps.google.com', 'storage.googleapis.com']:
                        assert fixture.request(plan['ports'][name], f'http://{host}:{origin.server_port}/page') == (200, name)
                    for host in hosts[:7]:
                        assert fixture.request(plan['ports'][name], f'http://{host}:{origin.server_port}/ai') == (200, 'Locked')
                checks.append('concurrent Chrome node ports retain independent ordinary-page exits and prioritize AI domains')

                assert fixture.request(mixed, f'http://api.openai.com:{origin.server_port}/main') == (200, 'Locked')
                assert fixture.request(plan['direct'], f'http://api.openai.com:{origin.server_port}/guarded-direct') == (200, 'Locked')
                direct = subprocess.run([str(executable), 'fetch', f'http://subscription.invalid:{origin.server_port}/subscription',
                                         str(plan['direct'])], capture_output=True, text=True)
                assert direct.returncode == 0, direct.stdout + direct.stderr
                checks.append('main and managed DIRECT ports honor AI lock while normal subscription refresh stays direct')

                def toggle(enabled):
                    state = json.loads((directory / 'state.json').read_text())
                    for node in state['registry']['profiles']['fixture']:
                        if node['name'] == 'Beta':
                            node['enabled'] = enabled
                    (directory / 'state.json').write_text(json.dumps(state))
                    updated = generate()
                    call('/configs', {'path': str(directory / 'runtime.json')}, 'PUT')
                    return updated

                stopped = toggle(False)
                assert stopped['ports']['Beta'] == plan['ports']['Beta']
                for _ in range(20):
                    if not is_open(plan['ports']['Beta']):
                        break
                    time.sleep(0.05)
                assert not is_open(plan['ports']['Beta'])
                assert fixture.request(plan['ports']['Alpha'], 'http://github.com/still-running') == (200, 'Alpha')
                resumed = toggle(True)
                assert resumed['ports']['Beta'] == plan['ports']['Beta']
                assert fixture.request(plan['ports']['Beta'], 'http://github.com/resumed') == (200, 'Beta')
                checks.append('individual stop closes only that listener and restart retains its original port')

                with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sink:
                    sink.bind(('127.0.0.1', 0))
                    sink.settimeout(0.7)
                    destination = sink.getsockname()[1]
                    send_udp(control, 'udp-origin.invalid', destination, b'control')
                    assert sink.recvfrom(1024)[0] == b'control', 'UDP fixture control did not reach its local origin'
                    send_udp(plan['ports']['Alpha'], 'api.openai.com', destination, b'protected')
                    try:
                        leaked = sink.recvfrom(1024)
                        raise AssertionError('AI UDP fell through to DIRECT: ' + repr(leaked))
                    except socket.timeout:
                        pass
                    send_udp(plan['ports']['Alpha'], 'udp-origin.invalid', destination, b'ordinary')
                    try:
                        leaked = sink.recvfrom(1024)
                        raise AssertionError('unsupported node UDP fell through to DIRECT: ' + repr(leaked))
                    except socket.timeout:
                        pass
                checks.append('unsupported UDP is rejected for AI and fixed-node traffic without a DIRECT fallback')

                locked.shutdown()
                locked.server_close()
                before = len(alpha.requests) + len(beta.requests) + len(origin.requests)
                try:
                    status, _ = fixture.request(plan['ports']['Alpha'], f'http://api.openai.com:{origin.server_port}/failed')
                    assert status >= 400
                except (OSError, TimeoutError):
                    pass
                assert len(alpha.requests) + len(beta.requests) + len(origin.requests) == before
                checks.append('failed locked node never falls back to the Chrome-selected node or DIRECT')
        print(json.dumps({'passed': True, 'checks': checks}, ensure_ascii=False, indent=2))
    finally:
        if core is not None and core.poll() is None:
            core.terminate()
            core.wait(timeout=8)
        for server in servers:
            server.shutdown()
            server.server_close()


if __name__ == '__main__':
    main()
