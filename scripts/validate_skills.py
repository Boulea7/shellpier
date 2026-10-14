#!/usr/bin/env python3
"""Validate bundled skills without requiring an external skill package."""
from pathlib import Path
import re

root = Path(__file__).resolve().parents[1]
skills = list((root / 'skills').glob('*/SKILL.md'))
if not skills:
    raise SystemExit('No bundled skills found')
for path in skills:
    text = path.read_text(encoding='utf-8')
    if not text.startswith('---\n') or '\n---\n' not in text[4:]:
        raise SystemExit(f'{path}: missing YAML frontmatter')
    frontmatter = text.split('---', 2)[1]
    name = re.search(r'^name:\s*([a-z0-9-]+)\s*$', frontmatter, re.M)
    if not name or name[1] != path.parent.name or len(name[1]) > 64:
        raise SystemExit(f'{path}: invalid skill name')
    if not re.search(r'^description:\s*\S.+$', frontmatter, re.M):
        raise SystemExit(f'{path}: missing description')
    if re.search(r'\b(TODO|TBD|FIXME)\b|\[INSERT', text):
        raise SystemExit(f'{path}: unfinished scaffold')
    for target in re.findall(r'\]\(([^)]+)\)', text):
        if '://' not in target and not target.startswith('#'):
            if not (path.parent / target.split('#')[0]).exists():
                raise SystemExit(f'{path}: missing reference {target}')
print(f'Validated {len(skills)} skill(s)')
