"""Isolated binary lifecycle checks. Uses only synthetic credentials and loopback HTTP."""
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.request import Request, build_opener, ProxyHandler

ROOT = Path(__file__).resolve().parent.parent
BINARY = ROOT / 'bin/gatt'
HTTP = build_opener(ProxyHandler({}))


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


class Upstream(BaseHTTPRequestHandler):
    calls = 0
    stream = True

    def log_message(self, *_):
        pass

    def do_POST(self):
        self.rfile.read(int(self.headers['Content-Length']))
        if self.headers.get('Authorization') != 'Bearer runtime-synthetic-secret':
            self.send_error(401)
            return
        Upstream.calls += 1
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream' if Upstream.stream else 'application/json')
        self.end_headers()
        if not Upstream.stream:
            self.wfile.write(b'{"id":"runtime-complete","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}')
            return
        try:
            while True:
                self.wfile.write(b': heartbeat\n\n')
                self.wfile.flush()
                time.sleep(.05)
        except (BrokenPipeError, ConnectionResetError):
            pass


def request(base, path, method='GET', body=None, session=None, key=None):
    headers = {'Origin': base, 'Content-Type': 'application/json'}
    if session:
        headers['Authorization'] = 'Bearer ' + session
    if key:
        headers['Authorization'] = 'Bearer ' + key
    req = Request(base + path, json.dumps(body).encode() if body is not None else None, headers, method=method)
    try:
        response = HTTP.open(req, timeout=5)
    except HTTPError as error:
        response = error
    with response:
        return response.status, response.headers, json.loads(response.read())


def main():
    processes = []
    upstream = ThreadingHTTPServer(('127.0.0.1', 0), Upstream)
    upstream.daemon_threads = True
    threading.Thread(target=upstream.serve_forever, daemon=True).start()
    with tempfile.TemporaryDirectory(prefix='gatt-runtime-') as temp:
        temp = Path(temp)
        # Prevent test instances from opening browser tabs; production launch behavior is unchanged.
        launchers = temp / 'launchers'
        launchers.mkdir()
        (launchers / 'open').write_text('#!/bin/sh\nexit 0\n')
        (launchers / 'open').chmod(0o700)
        env = dict(os.environ, PATH=str(launchers) + os.pathsep + os.environ['PATH'])

        def config_for(data, name):
            cfg = json.loads((ROOT / 'config.example.json').read_text())
            cfg.update(listen=f'127.0.0.1:{free_port()}', data_dir=str(data))
            path = temp / (name + '.json')
            path.write_text(json.dumps(cfg))
            return path, 'http://' + cfg['listen']

        def launch(config, base):
            proc = subprocess.Popen([str(BINARY), '-config', str(config), 'serve'], env=env,
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            processes.append(proc)
            for _ in range(100):
                if proc.poll() is not None:
                    raise AssertionError('isolated server exited during startup')
                try:
                    if request(base, '/healthz')[0] == 200:
                        return proc
                except (URLError, TimeoutError, ConnectionError):
                    pass
                time.sleep(.05)
            raise AssertionError('isolated startup timed out')

        def stop(proc):
            proc.terminate()
            proc.wait(timeout=8)

        def login(base, data):
            secret = json.loads((data / 'secrets/credentials.json').read_text())['administrator']
            native = Request(base + '/admin/browser-tickets', data=b'', headers={'Authorization': 'Bearer '+secret}, method='POST')
            with HTTP.open(native, timeout=5) as response:
                ticket = json.load(response)['ticket']
            code, _, result = request(base, '/admin/session', 'POST', {'ticket': ticket})
            assert code == 200, 'file credential login failed'
            return result['session_token']


        try:
            data = temp / 'original'
            config, base = config_for(data, 'original')
            proc = launch(config, base)
            session = login(base, data)
            status, _, account = request(base, '/admin/accounts', 'POST', {'provider':'openai_compatible','auth_type':'api_key','name':'Runtime synthetic'}, session=session)
            assert status == 201, 'account creation failed'
            status, _, _ = request(base, '/admin/accounts/'+account['id']+'/credential', 'POST', {'version':account['version'],'secret':'runtime-synthetic-secret'}, session=session)
            assert status == 200, 'credential publication failed'
            status, _, source = request(base, '/admin/sources', 'POST', {
                'name': 'Runtime synthetic', 'account_id':account['id'],
                'base_url': f'http://127.0.0.1:{upstream.server_port}',
                'models': ['runtime-model'],
            }, session=session)
            assert status == 201, 'source creation failed'
            status, _, key = request(base, '/admin/client-keys', 'POST', {
                'name': 'Runtime client', 'source_id': source['id'],
            }, session=session)
            assert status == 201, 'client key creation failed'
            collision, _ = config_for(data, 'collision')
            blocked = subprocess.run([str(BINARY), '-config', str(collision), 'recover-admin'],
                                     env=env, capture_output=True, timeout=5)
            assert blocked.returncode != 0 and '数据目录'.encode() in blocked.stderr, 'same directory admitted twice'
            payload = {'model': 'runtime-model', 'input': 'synthetic', 'stream': True}
            stream = HTTP.open(Request(base + '/v1/responses', json.dumps(payload).encode(), {
                'Authorization': 'Bearer ' + key['secret'], 'Content-Type': 'application/json',
            }), timeout=5)
            assert stream.readline().startswith(b':'), 'stream did not start'
            proc.kill()
            proc.wait(timeout=5)
            stream.close()
            proc = launch(config, base)
            session = login(base, data)
            status, _, records = request(base, '/admin/requests', session=session)
            assert status == 200 and len(records['items']) == 1, 'crash record missing'
            assert records['items'][0]['status'] == 'interrupted', 'crash reported as success'
            assert Upstream.calls == 1, 'startup replayed the interrupted call'
            stop(proc)

            restored = temp / 'restored'
            shutil.copytree(data, restored)
            restored_config, restored_base = config_for(restored, 'restored')
            proc = launch(restored_config, restored_base)
            login(restored_base, restored)
            Upstream.stream = False
            payload['stream'] = False
            status, _, result = request(restored_base, '/v1/responses', 'POST', payload, key=key['secret'])
            assert status == 200 and result['status'] == 'completed', 'complete backup restore failed'
            stop(proc)

            metadata = temp / 'metadata-only'
            shutil.copytree(restored, metadata, ignore=shutil.ignore_patterns('secrets'))
            metadata_config, metadata_base = config_for(metadata, 'metadata')
            proc = launch(metadata_config, metadata_base)
            assert request(metadata_base, '/admin/sources')[0] == 401, 'missing admin opened anonymous management'
            calls = Upstream.calls
            assert request(metadata_base, '/v1/responses', 'POST', payload, key=key['secret'])[0] == 503, 'missing source credential dispatched'
            assert Upstream.calls == calls, 'upstream called without source credential'
            stop(proc)
            recovered = subprocess.run([str(BINARY), '-config', str(metadata_config), 'recover-admin'],
                                       env=env, capture_output=True, timeout=5)
            assert recovered.returncode == 0, 'explicit admin recovery failed'
            proc = launch(metadata_config, metadata_base)
            session = login(metadata_base, metadata)
            status, _, sources = request(metadata_base, '/admin/sources', session=session)
            assert status == 200 and sources[0]['id'] == source['id'], 'source metadata lost during recovery'
            assert request(metadata_base, '/v1/responses', 'POST', payload, key=key['secret'])[0] == 503, 'admin recovery silently invented source credentials'
            report = {
                'fresh_directory_startup': True, 'same_directory_second_process_rejected': True,
                'sigkill_recovers_interrupted_without_replay': True, 'full_backup_restore': True,
                'metadata_only_restore_stays_locked': True, 'explicit_admin_recovery': True,
                'missing_source_credential_blocks_dispatch': True,
                'scope': 'isolated local binary processes and synthetic loopback provider; no real account or cross-machine claim',
            }
            (ROOT / 'test-results').mkdir(exist_ok=True)
            (ROOT / 'test-results/runtime-acceptance.json').write_text(json.dumps(report, indent=2))
            print(json.dumps(report, indent=2))
        finally:
            for proc in processes:
                if proc.poll() is None:
                    proc.kill()
                    proc.wait(timeout=5)
                if proc.stdout:
                    proc.stdout.close()
                if proc.stderr:
                    proc.stderr.close()
            upstream.shutdown()
            upstream.server_close()


if __name__ == '__main__':
    main()
