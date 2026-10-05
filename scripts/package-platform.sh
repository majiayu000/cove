#!/usr/bin/env bash
set -euo pipefail
# Native, local development artifact. Existing build-evidence.py remains the
# canonical source and embedded-artifact evidence; no target release is implied.
cd "$(dirname "$0")/.."
python3 - <<'PY'
import hashlib, io, json, os, platform, subprocess, sys, tarfile
from pathlib import Path
root = Path.cwd()
sha = lambda data: hashlib.sha256(data).hexdigest()
def goenv(key): return subprocess.check_output(['go','env',key],text=True).strip()
host_os, host_arch = goenv('GOHOSTOS'), goenv('GOHOSTARCH')
if (goenv('GOOS'),goenv('GOARCH')) != (host_os,host_arch):
    raise SystemExit('Native package only: cross-target CGO/runtime validation is not available here.')
evidence = json.loads((root/'bin/build-evidence.json').read_text())
source_id = subprocess.check_output([sys.executable,'scripts/build-evidence.py','source-id'],text=True).strip()
if evidence['source_id'] != source_id: raise SystemExit('Source changed after build; run make build before packaging.')
binary = 'bin/gatt.exe' if host_os == 'windows' else 'bin/gatt'
info = subprocess.check_output(['go','version','-m',binary],text=True)
if f'GOOS={host_os}' not in info or f'GOARCH={host_arch}' not in info:
    raise SystemExit('Binary target does not match this native package host.')
if f'gatt/internal/app.BuildID={source_id}' not in info:
    raise SystemExit('Binary build ID does not match build evidence.')
contents = {}
for name, expected in evidence['artifact_sha256'].items():
    p = Path(name)
    if p.is_absolute() or '..' in p.parts or p.is_symlink() or not p.is_file():
        raise SystemExit('Artifact path is unsafe or missing: '+name)
    data = p.read_bytes()
    if sha(data) != expected: raise SystemExit('Artifact changed after build: '+name)
    contents[name] = data
contents['bin/build-evidence.json'] = (root/'bin/build-evidence.json').read_bytes()
for name in ['README.md','Cove.command','config.example.json','docs/first-request.md','docs/platform-commands.md','docs/enterprise.md','docs/spec/04-ENTERPRISE-SINGLE-SERVER.md','docs/implementation.md','docs/implementation-readiness.tsv','docs/spec/README.md']:
    contents[name] = (root/name).read_bytes()
license_status = 'unlicensed-development-artifact'
if (root/'LICENSE').is_file():
    contents['LICENSE'] = (root/'LICENSE').read_bytes()
    license_status = 'MIT'
manifest = dict(format='cove-local-package-v1',version='0.1.0-dev',build_id=source_id,
    source_id=source_id,go_version=info.splitlines()[0].split()[-1],
    node_version=evidence['node_version'],os=host_os,arch=host_arch,
    adapter_version='source-'+source_id,web_embedded=True,manual_only=True,
    license=license_status,min_data_schema=0,max_data_schema=2,data_schema=2,
    files={name:dict(sha256=sha(data),size=len(data)) for name,data in sorted(contents.items())})
contents['manifest.json'] = (json.dumps(manifest,ensure_ascii=False,indent=2)+'\n').encode()
output = root/'bin'/f'cove-development-{host_os}-{host_arch}.tar.gz'
with tarfile.open(output,'w:gz') as archive:
    for name,data in sorted(contents.items()):
        entry=tarfile.TarInfo(name);entry.size=len(data);entry.mode=0o700 if name in (binary,'Cove.command') else 0o600
        archive.addfile(entry,io.BytesIO(data))
digest=sha(output.read_bytes());(Path(str(output)+'.sha256')).write_text(digest+'  '+output.name+'\n')
print(output)
print('SHA-256:',digest)
print('Manual-only development artifact; native install/restart/ACL/browser validation remains separate.')
PY
