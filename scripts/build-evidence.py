#!/usr/bin/env python3
"""Identify the uncommitted source and the exact assets embedded in a local build."""
import hashlib
import io
import json
import os
from pathlib import Path
import sys
import subprocess
import platform
from datetime import datetime, timezone
import tarfile

ROOT = Path(__file__).resolve().parents[1]


def sha(data):
    return hashlib.sha256(data).hexdigest()


def sources():
    paths = [ROOT / name for name in (
        '.gitignore', 'Cove.command', 'Makefile', 'README.md',
        'config.example.json', 'go.mod', 'go.sum',
    )]
    for name in ('cmd', 'internal', 'web', 'scripts', 'docs'):
        for base, dirs, files in os.walk(ROOT / name):
            dirs[:] = sorted(d for d in dirs if d not in ('node_modules', 'dist', '__pycache__', '.git'))
            paths.extend(Path(base) / f for f in files if not f.startswith('.'))
    if (ROOT / 'LICENSE').is_file():
        paths.append(ROOT / 'LICENSE')
    return {p.relative_to(ROOT).as_posix(): p.read_bytes() for p in sorted(paths)}


def main():
    contents = sources()
    hashes = {name: sha(data) for name, data in contents.items()}
    source_id = sha(json.dumps(hashes, sort_keys=True, separators=(',', ':')).encode())
    if sys.argv[1:] == ['source-id']:
        print(source_id)
        return
    if len(sys.argv) != 3 or sys.argv[1] != 'record':
        raise SystemExit('usage: build-evidence.py source-id | record SOURCE_ID')
    if sys.argv[2] != source_id:
        raise SystemExit('Source changed during build; rebuild before recording evidence.')
    snapshot = ROOT / 'bin/source-snapshot.tar.gz'
    with tarfile.open(snapshot, 'w:gz') as archive:
        for name, data in contents.items():
            entry = tarfile.TarInfo(name)
            entry.size = len(data)
            entry.mode = (ROOT / name).stat().st_mode & 0o777
            archive.addfile(entry, io.BytesIO(data))
    binary = ROOT / ('bin/gatt.exe' if sys.platform == 'win32' else 'bin/gatt')
    artifacts = [binary, snapshot]
    artifacts += sorted(p for p in (ROOT / 'web/dist').rglob('*') if p.is_file())
    def command(*args):
        return subprocess.check_output(args, cwd=ROOT, text=True).strip()
    git = subprocess.run(['git','rev-parse','HEAD'],cwd=ROOT,text=True,capture_output=True)
    evidence = {
        'format': 'cove-build-evidence-v2',
        'built_at': datetime.now(timezone.utc).isoformat(),
        'git_sha': git.stdout.strip() if git.returncode == 0 else None,
        'git_state': 'working source snapshot; no committed HEAD' if git.returncode else 'working source snapshot',
        'go_version': command('go','version'),
        'node_version': command('node','--version'),
        'npm_version': command('npm.cmd' if sys.platform == 'win32' else 'npm','--version'),
        'os': command('go','env','GOOS'),
        'arch': command('go','env','GOARCH'),
        'host': platform.system()+' '+platform.release(),
        'adapter_version': 'source-'+source_id,
        'source_id': source_id,
        'source_sha256': hashes,
        'artifact_sha256': {p.relative_to(ROOT).as_posix(): sha(p.read_bytes()) for p in artifacts},
        'runtime': 'Not restarted or verified by this build command.',
        'checks': 'Build only; test evidence is recorded separately in docs/implementation.md.',
    }
    (ROOT / 'bin/build-evidence.json').write_text(json.dumps(evidence, ensure_ascii=False, indent=2) + '\n')
    print('Build source:', source_id)


if __name__ == '__main__':
    main()
