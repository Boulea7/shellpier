#!/usr/bin/env python3
"""Cross-build offline release archives and a SHA-256 manifest."""
import argparse
import hashlib
from pathlib import Path
import os
import subprocess
import tempfile
import zipfile

parser = argparse.ArgumentParser()
parser.add_argument('--version', required=True)
parser.add_argument('--out', default='dist')
args = parser.parse_args()
if not args.version or any(c not in 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-_' for c in args.version):
    parser.error('version must contain only letters, digits, dot, dash or underscore')
root = Path(__file__).resolve().parents[1]
out = Path(args.out).resolve()
out.mkdir(parents=True, exist_ok=True)
targets = [('windows','amd64'),('windows','386'),('windows','arm64'),('linux','amd64'),('linux','arm64'),('darwin','amd64'),('darwin','arm64')]
checksums = []
for system, arch in targets:
    with tempfile.TemporaryDirectory(prefix='shellpier-release-') as work:
        binary = Path(work) / ('shellpier.exe' if system == 'windows' else 'shellpier')
        env = dict(os.environ, GOOS=system, GOARCH=arch, CGO_ENABLED='0')
        subprocess.run(['go','build','-trimpath','-ldflags',f'-X main.version={args.version}','-o',str(binary),'./cmd/shellpier'],cwd=root,env=env,check=True)
        archive = out / f'shellpier_{args.version}_{system}_{arch}.zip'
        with zipfile.ZipFile(archive,'w',zipfile.ZIP_DEFLATED) as z:
            z.write(binary,binary.name)
            if system == 'windows':
                z.write(root/'scripts/START.cmd','START.cmd')
            elif system == 'darwin':
                z.write(root/'scripts/start.command','start.command')
            for name in ['LICENSE','README.md','README.zh-CN.md','CONTRIBUTING.md','SECURITY.md','CODE_OF_CONDUCT.md','CHANGELOG.md']:
                z.write(root/name,name)
            for directory in ['skills','legacy','scripts','docs']:
                path = root/directory
                if path.exists():
                    for file in sorted(path.rglob('*')):
                        if file.is_file() and '__pycache__' not in file.parts:
                            z.write(file,str(file.relative_to(root)))
        checksums.append(f'{hashlib.sha256(archive.read_bytes()).hexdigest()}  {archive.name}')
(out/'checksums.txt').write_text('\n'.join(checksums)+'\n')
print(f'Built {len(targets)} archives in {out}')
