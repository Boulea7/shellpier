#!/usr/bin/env python3
"""Cross-build offline archives with the verified Windows runtime backport."""
import argparse
from pathlib import Path
import json
import os
import subprocess
import tempfile
import zipfile
import sys

from patch_go_windows import UPSTREAM, patch

TARGETS = [('windows', 'amd64'), ('windows', '386'), ('windows', 'arm64'), ('linux', 'amd64'), ('linux', 'arm64'), ('darwin', 'amd64'), ('darwin', 'arm64')]
RUNTIME_SYMBOL = 'github.com/Boulea7/shellpier/internal/cli.runtimePatch'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--version', required=True)
    parser.add_argument('--out', default='dist')
    args = parser.parse_args()
    if not args.version or any(c not in 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-_' for c in args.version):
        parser.error('version must contain only letters, digits, dot, dash or underscore')
    root = Path(__file__).resolve().parents[1]
    out = Path(args.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    for system, arch in TARGETS:
        archive = out / f'shellpier_{args.version}_{system}_{arch}.zip'
        if archive.exists():
            parser.error(f'refusing to replace an existing archive: {archive}')
    # Keep the patched toolchain and its cache outside the checkout. All three
    # Windows architectures are built from the same independently verified copy.
    with tempfile.TemporaryDirectory(prefix='shellpier-release-') as work:
        work = Path(work)
        stock_env = dict(os.environ, GOTOOLCHAIN='local')
        # Packaging must start with a stock installation, never a previously patched GOROOT.
        stock_env.pop('GOROOT', None)
        go_info = json.loads(subprocess.check_output(['go', 'env', '-json', 'GOROOT', 'GOVERSION'], cwd=root, env=stock_env))
        if go_info['GOVERSION'] != 'go1.27.2':
            raise ValueError('release packaging requires stock Go1.27.2')
        stock_root = Path(go_info['GOROOT'])
        stock_go = str(stock_root / 'bin' / ('go.exe' if os.name == 'nt' else 'go'))
        patched_root = patch(stock_root, work / 'go-windows', work / 'patch-evidence')
        patched_go = str(patched_root / 'bin' / ('go.exe' if os.name == 'nt' else 'go'))
        manifest = json.loads((patched_root / 'shellpier-windows-patch.json').read_text())
        for system, arch in TARGETS:
            binary = work / ('shellpier.exe' if system == 'windows' else 'shellpier')
            env = dict(stock_env, GOOS=system, GOARCH=arch, CGO_ENABLED='0', GOROOT=str(stock_root))
            go = stock_go
            ldflags = f'-X main.version={args.version}'
            if system == 'windows':
                go = patched_go
                env.update(GOROOT=str(patched_root), GOCACHE=str(work / 'go-windows-cache'))
                ldflags += f' -X {RUNTIME_SYMBOL}={UPSTREAM}'
            subprocess.run([go, 'build', '-trimpath', '-ldflags', ldflags, '-o', str(binary), './cmd/shellpier'], cwd=root, env=env, check=True)
            archive = out / f'shellpier_{args.version}_{system}_{arch}.zip'
            with zipfile.ZipFile(archive, 'x', zipfile.ZIP_DEFLATED) as z:
                z.write(binary, binary.name)
                if system == 'windows':
                    z.writestr('runtime-patch.json', json.dumps(dict(manifest, goos=system, goarch=arch, ldflags=ldflags), indent=2) + '\n')
                    z.write(root / 'scripts/START.cmd', 'START.cmd')
                elif system == 'darwin':
                    z.write(root / 'scripts/start.command', 'start.command')
                public_files = ['LICENSE', 'CONTRIBUTING.md', 'CODE_OF_CONDUCT.md', 'CHANGELOG.md', 'SUPPORT.md', 'THIRD_PARTY_NOTICES.md', 'go.mod', 'go.sum']
                public_files.extend(path.name for path in sorted(root.glob('README*.md')))
                public_files.extend(path.name for path in sorted(root.glob('SECURITY*.md')))
                for name in public_files:
                    z.write(root / name, name)
                for directory in ['skills', 'legacy', 'scripts', 'docs', 'deploy', 'assets']:
                    path = root / directory
                    if path.exists():
                        for file in sorted(path.rglob('*')):
                            if file.is_file() and file.name not in {'AGENTS.md', 'CLAUDE.md'} and '__pycache__' not in file.parts:
                                z.write(file, str(file.relative_to(root)))
            with tempfile.TemporaryDirectory(prefix='shellpier-docs-') as unpacked:
                with zipfile.ZipFile(archive) as z:
                    z.extractall(unpacked)
                subprocess.run([sys.executable, str(root / 'scripts/validate_docs.py'), '--root', unpacked], check=True)
    print(f'Built {len(TARGETS)} archives in {out}')


if __name__ == '__main__':
    main()
