"""Native current-user service acceptance; refuses an existing registration."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import socket
import subprocess
import tempfile
import time
from urllib.request import build_opener, ProxyHandler, Request

ROOT = Path(__file__).resolve().parent.parent
HTTP = build_opener(ProxyHandler({}))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', default=str(ROOT / 'bin/gatt'))
    parser.add_argument('--output', default=str(ROOT / 'test-results/service-acceptance.json'))
    args = parser.parse_args()
    if platform.system() != 'Darwin':
        raise SystemExit('This script verifies native launchd; Linux/Windows require their real user managers.')
    registration = Path.home() / 'Library/LaunchAgents/com.cove.gatt.plist'
    if registration.exists() or registration.is_symlink():
        raise SystemExit('Existing Cove registration; refusing to overwrite it.')
    result = {'os': platform.platform(), 'arch': platform.machine(), 'checks': {}, 'passed': False}
    temp = Path(tempfile.mkdtemp(prefix='cove-service-')).resolve()
    private = temp / 'Cove 中文 空格'
    private.mkdir(mode=0o700)
    binary = private / 'gatt'
    shutil.copy2(args.binary, binary)
    binary.chmod(0o700)
    result['binary_sha256'] = hashlib.sha256(binary.read_bytes()).hexdigest()
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    config = json.loads((ROOT / 'config.example.json').read_text())
    data = private / '数据 目录'
    config.update(listen=f'127.0.0.1:{port}', data_dir=str(data))
    path = private / '配置 文件.json'
    path.write_text(json.dumps(config))
    path.chmod(0o600)
    env = dict(os.environ, PATH='/usr/bin:/bin:/usr/sbin:/sbin')
    installed = False
    uninstalled = False

    def command(*command, ok=True):
        proc = subprocess.run([str(binary), '-config', str(path), *command], env=env, text=True, capture_output=True, timeout=40)
        if ok and proc.returncode:
            raise AssertionError('Command failed: ' + ' '.join(command) + ': ' + proc.stderr.strip())
        return proc

    def status():
        return json.loads(command('status').stdout)

    def await_state(running):
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            value = status()
            if value['running'] == running and (not running or value['verified'] and value['ready']):
                return value
            time.sleep(.25)
        raise AssertionError('Native manager did not reach expected state')

    def management(path, method='GET', body=None, session=None):
        base = f'http://{config["listen"]}'
        headers = {'Origin': base}
        if session:
            headers['Authorization'] = 'Bearer ' + session
        content = None if body is None else json.dumps(body).encode()
        if content is not None:
            headers['Content-Type'] = 'application/json'
        with HTTP.open(Request(base + '/admin/' + path, data=content, method=method, headers=headers), timeout=3) as response:
            return json.load(response)

    try:
        initial = status()
        assert not initial['registered'] and not initial['running']
        command('service', 'install')
        installed = True
        assert status()['registered'] and not status()['running']
        result['checks']['install_does_not_launch'] = True
        command('service', 'start')
        first = await_state(True)
        result['build_id'] = first['instance']['build_id']
        result['checks']['native_manager_pid_port_build_verified'] = first['manager_pid'] == first['instance']['pid'] == first['port_owner_pid']
        with HTTP.open(f'http://{config["listen"]}/', timeout=3) as page:
            assert page.status == 200 and b'<div id="root">' in page.read()
        result['checks']['embedded_web_without_go_node'] = True
        assert data.stat().st_mode & 0o777 == 0o700
        assert (data / 'secrets/credentials.json').stat().st_mode & 0o777 == 0o600
        result['checks']['private_acl_chinese_space_paths'] = True
        different = private / 'other.json'
        different.write_text(json.dumps(dict(config, data_dir=str(private / 'other-data'))))
        collision = subprocess.run([str(binary), '-config', str(different), 'service', 'install'], env=env, text=True, capture_output=True, timeout=10)
        assert collision.returncode != 0
        assert await_state(True)['instance']['pid'] == first['instance']['pid']
        result['checks']['registration_conflict_preserves_running_instance'] = True
        command('service', 'stop')
        await_state(False)
        assert (data / 'gatt.db').exists()
        result['checks']['stop_releases_listener_preserves_data'] = True
        command('service', 'start')
        second = await_state(True)
        assert second['instance']['pid'] != first['instance']['pid']
        result['checks']['restart_new_pid_same_build'] = second['instance']['build_id'] == result['build_id']
        # Terminate only the PID proven to belong to this temporary service.
        assert second['verified'] and second['instance']['data_dir'] == str(data)
        session = management('session', 'POST', {})['session_token']
        account = management('accounts', 'POST', {'provider': 'local', 'auth_type': 'none', 'name': 'native-crash-fixture'}, session)
        management('session', 'DELETE', session=session)
        before_crash = hashlib.sha256((data / 'secrets/credentials.json').read_bytes()).hexdigest()
        os.kill(second['instance']['pid'], signal.SIGKILL)
        await_state(False)
        command('service', 'start')
        recovered = await_state(True)
        result['checks']['forced_process_exit_then_native_start_recovers'] = recovered['instance']['pid'] != second['instance']['pid'] and recovered['instance']['build_id'] == result['build_id']
        result['checks']['crash_recovery_preserves_credentials_and_database'] = before_crash == hashlib.sha256((data / 'secrets/credentials.json').read_bytes()).hexdigest() and (data / 'gatt.db').exists()
        session = management('session', 'POST', {})['session_token']
        persisted = management('accounts/' + account['id'], session=session)
        management('session', 'DELETE', session=session)
        result['checks']['crash_recovery_preserves_persisted_account'] = all(persisted[field] == account[field] for field in ('id', 'name', 'provider', 'auth_type', 'version'))
        command('service', 'uninstall')
        uninstalled = True
        assert not registration.exists() and not await_state(False)['registered']
        assert (data / 'gatt.db').exists()
        result['checks']['uninstall_preserves_data'] = True
        result['passed'] = all(result['checks'].values())
    except Exception as error:
        result['error'] = str(error)
    finally:
        if installed and not uninstalled:
            try:
                command('service', 'uninstall')
                uninstalled = True
            except Exception as error:
                result['cleanup_error'] = str(error)
        if not installed or uninstalled:
            shutil.rmtree(temp)
        else:
            result['retained_private_directory'] = str(temp)
        output = Path(args.output)
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
        print(json.dumps(result, ensure_ascii=False, indent=2))
    if not result['passed']:
        raise SystemExit(1)


if __name__ == '__main__':
    main()
