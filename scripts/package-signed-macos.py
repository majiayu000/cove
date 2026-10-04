#!/usr/bin/env python3
"""Create a signed, notarized DMG candidate from the verified native package."""
import argparse
import hashlib
import json
from pathlib import Path
import platform
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parent.parent


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--identity', required=True, help='Developer ID Application certificate name or SHA-1')
    parser.add_argument('--notary-profile', required=True, help='Existing notarytool Keychain profile; never a password')
    args = parser.parse_args()
    if platform.system() != 'Darwin':
        raise SystemExit('This command requires a native macOS build host.')
    evidence = json.loads((ROOT / 'bin/build-evidence.json').read_text())
    if evidence['os'] != 'darwin':
        raise SystemExit('Build evidence is not for macOS.')
    # Reuse the package gate: source, embedded assets and binary must match.
    subprocess.run(['bash', 'scripts/package-platform.sh'], cwd=ROOT, check=True)
    archive = ROOT / 'bin' / f'cove-development-darwin-{evidence["arch"]}.tar.gz'
    output = ROOT / 'bin' / f'cove-signed-darwin-{evidence["arch"]}-{evidence["source_id"][:12]}.dmg'
    if output.exists():
        raise SystemExit('Signed candidate already exists; preserve it or choose a fresh build.')
    report = {'build_id': evidence['source_id'], 'arch': evidence['arch'], 'passed': False,
              'scope': 'Signed/notarized distribution candidate; provider and desktop acceptance remain separate.'}
    run = lambda command: subprocess.run(command, cwd=ROOT, check=True)
    try:
        with tempfile.TemporaryDirectory(prefix='cove-signing-') as directory:
            staging = Path(directory)
            payload = staging / 'Cove'
            payload.mkdir()
            with tarfile.open(archive) as package:
                package.extractall(payload, filter='data')
            binary = payload / 'bin/gatt'
            run(['codesign', '--force', '--options', 'runtime', '--timestamp', '--identifier', 'com.cove.gatt', '--sign', args.identity, str(binary)])
            run(['codesign', '--verify', '--strict', str(binary)])
            manifest = json.loads((payload / 'manifest.json').read_text())
            digest = hashlib.sha256(binary.read_bytes()).hexdigest()
            manifest['files']['bin/gatt'] = {'sha256': digest, 'size': binary.stat().st_size}
            packaged_evidence = json.loads((payload / 'bin/build-evidence.json').read_text())
            packaged_evidence['artifact_sha256']['bin/gatt'] = digest
            packaged_evidence['distribution'] = 'Developer ID signed candidate; see external notarization report'
            data = (json.dumps(packaged_evidence, ensure_ascii=False, indent=2) + '\n').encode()
            (payload / 'bin/build-evidence.json').write_bytes(data)
            manifest['files']['bin/build-evidence.json'] = {'sha256': hashlib.sha256(data).hexdigest(), 'size': len(data)}
            (payload / 'manifest.json').write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + '\n')
            candidate = staging / output.name
            run(['hdiutil', 'create', '-srcfolder', str(payload), '-volname', 'Cove', '-format', 'UDZO', str(candidate)])
            run(['codesign', '--timestamp', '--sign', args.identity, str(candidate)])
            run(['codesign', '--verify', '--strict', str(candidate)])
            submission = subprocess.run(['xcrun', 'notarytool', 'submit', str(candidate), '--keychain-profile', args.notary_profile,
                                         '--wait', '--timeout', '15m', '--output-format', 'json'], cwd=ROOT, text=True, capture_output=True)
            # Keep public submission status, never Keychain contents or credentials.
            try:
                report['notarization'] = json.loads(submission.stdout)
            except json.JSONDecodeError:
                raise RuntimeError('Notarytool did not return a JSON status; check the selected profile locally.')
            if submission.returncode or report['notarization'].get('status') != 'Accepted':
                raise RuntimeError('Notarization not accepted; no signed release artifact produced.')
            run(['xcrun', 'stapler', 'staple', str(candidate)])
            run(['xcrun', 'stapler', 'validate', str(candidate)])
            run(['spctl', '--assess', '--type', 'open', '--context', 'context:primary-signature', str(candidate)])
            candidate.rename(output)
            digest = hashlib.sha256(output.read_bytes()).hexdigest()
            Path(str(output) + '.sha256').write_text(digest + '  ' + output.name + '\n')
            report.update(passed=True, filename=output.name, sha256=digest)
    finally:
        Path(str(output) + '.verification.json').write_text(json.dumps(report, indent=2) + '\n')
    print(output)


if __name__ == '__main__':
    main()
