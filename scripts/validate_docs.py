#!/usr/bin/env python3
"""Check public documentation's local links in a checkout or unpacked release."""
import argparse
from html.parser import HTMLParser
from pathlib import Path
import re
import sys
from urllib.parse import unquote, urlsplit
import xml.etree.ElementTree as ET

PUBLIC_DIRECTORIES = ('docs', 'assets', 'skills', 'legacy', 'scripts', 'deploy')
PRIVATE_NAMES = {'CLAUDE.md', 'GEMINI.md'}


def without_code(text):
    """Ignore fenced examples and inline code while retaining prose HTML."""
    lines = []
    fence = None
    for line in text.splitlines():
        marker = re.match(r'^\s{0,3}(`{3,}|~{3,})', line)
        if fence:
            if marker and marker[1][0] == fence[0] and len(marker[1]) >= len(fence):
                fence = None
            continue
        if marker:
            fence = marker[1]
            continue
        if line.startswith('    ') or line.startswith('\t'):
            continue
        lines.append(line)
    return re.sub(r'(`+).*?\1', '', '\n'.join(lines))


class Links(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.targets = []

    def handle_starttag(self, tag, attrs):
        self.targets.extend(value for key, value in attrs if key in ('href', 'src') and value)


def targets(path):
    text = path.read_text(encoding='utf-8')
    if path.suffix.lower() == '.svg':
        tree = ET.fromstring(text)
        for node in tree.iter():
            for key, value in node.attrib.items():
                if key in ('href', '{http://www.w3.org/1999/xlink}href'):
                    yield value
        return
    prose = without_code(text) if path.suffix.lower() == '.md' else text
    parser = Links()
    parser.feed(prose)
    yield from parser.targets
    if path.suffix.lower() == '.md':
        for match in re.finditer(r'!?\[[^\]\n]*\]\(\s*(?:<([^>\n]+)>|([^\s)]+))(?:\s+["\'][^\n]*?["\'])?\s*\)', prose):
            yield match[1] or match[2]
        for match in re.finditer(r'^\s{0,3}\[[^\]\n]+\]:\s*(?:<([^>\n]+)>|(\S+))', prose, re.M):
            yield match[1] or match[2]


def validate(root):
    root = root.resolve()
    files = {path for path in root.glob('*.md') if path.name not in PRIVATE_NAMES}
    files.update(root.glob('*.html'))
    # Public module guides are source-only; an unpacked release has no such tree.
    for directory in ('cmd', 'internal'):
        base = root / directory
        if base.exists():
            files.update(base.rglob('AGENTS.md'))
    for directory in PUBLIC_DIRECTORIES:
        base = root / directory
        if base.exists():
            files.update(path for path in base.rglob('*') if path.suffix.lower() in ('.md', '.html', '.svg') and path.name not in PRIVATE_NAMES and '__pycache__' not in path.parts)
    errors = []
    for path in sorted(files):
        try:
            for target in targets(path):
                parts = urlsplit(target)
                if parts.scheme or parts.netloc or not parts.path:
                    continue
                decoded = unquote(parts.path)
                destination = (root / decoded.lstrip('/') if decoded.startswith('/') else path.parent / decoded).resolve()
                try:
                    destination.relative_to(root)
                except ValueError:
                    errors.append(f'{path.relative_to(root)}: link escapes package root: {target}')
                    continue
                if not destination.exists():
                    errors.append(f'{path.relative_to(root)}: missing local target: {target}')
        except (OSError, UnicodeError, ET.ParseError, ValueError) as error:
            errors.append(f'{path.relative_to(root)}: {error}')
    if not files:
        errors.append('No public documentation found')
    for error in errors:
        print(error, file=sys.stderr)
    if errors:
        return 1
    print(f'Validated {len(files)} public Markdown/HTML/SVG files; external URLs were not fetched')
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=Path(__file__).resolve().parents[1])
    args = parser.parse_args()
    return validate(args.root)


if __name__ == '__main__':
    raise SystemExit(main())
