#!/usr/bin/env python3
"""Verify isolated backports reject version/source/destination drift."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

from patch_go_windows import ORIGINAL_SHA256, UPSTREAM, patch


class WindowsRuntimePatchTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        env = dict(os.environ, GOTOOLCHAIN='local')
        info = json.loads(subprocess.check_output(['go', 'env', '-json', 'GOROOT', 'GOVERSION'], env=env))
        if info['GOVERSION'] != 'go1.27.2':
            raise unittest.SkipTest('exact stock Go1.27.2 is required')
        cls.installed_root = Path(info['GOROOT'])
        if any(hashlib.sha256((cls.installed_root / name).read_bytes()).hexdigest() != expected for name, expected in ORIGINAL_SHA256.items()):
            raise unittest.SkipTest('stock Go1.27.2 source files are required')

    def setUp(self):
        self.work = tempfile.TemporaryDirectory(prefix='shellpier-patch-test-')
        self.base = Path(self.work.name)
        self.source = self.base / 'stock'
        self.source.mkdir()
        (self.source / 'VERSION').write_text('go1.27.2\n')
        for name in ORIGINAL_SHA256:
            target = self.source / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(self.installed_root / name, target)
        self.destination = self.base / 'patched'
        self.evidence = self.base / 'evidence'

    def tearDown(self):
        self.work.cleanup()

    def test_isolated_backport_and_idempotent_evidence(self):
        patch(self.source, self.destination, self.evidence)
        patch(self.source, self.destination, self.evidence)
        manifest = json.loads((self.evidence / 'manifest.json').read_text())
        self.assertEqual(manifest['upstream'], UPSTREAM)
        for entry in manifest['files']:
            name = entry['path']
            original = (self.source / name).read_bytes()
            modified = (self.destination / name).read_bytes()
            self.assertEqual(hashlib.sha256(original).hexdigest(), ORIGINAL_SHA256[name])
            self.assertEqual(entry['before_sha256'], hashlib.sha256(original).hexdigest())
            self.assertEqual(entry['after_sha256'], hashlib.sha256(modified).hexdigest())
            self.assertNotEqual(original, modified)
            self.assertEqual((self.evidence / 'after' / name).read_bytes(), modified)

    def test_source_drift_is_rejected_before_copy(self):
        target = self.source / 'src/runtime/os_windows.go'
        target.write_bytes(target.read_bytes() + b'\n')
        with self.assertRaisesRegex(ValueError, 'source hash mismatch'):
            patch(self.source, self.destination, self.evidence)
        self.assertFalse(self.destination.exists())

    def test_version_drift_is_rejected(self):
        (self.source / 'VERSION').write_text('go1.27.3\n')
        with self.assertRaisesRegex(ValueError, 'only stock'):
            patch(self.source, self.destination, self.evidence)

    def test_destination_drift_is_rejected(self):
        patch(self.source, self.destination, self.evidence)
        target = self.destination / 'src/runtime/os_windows.go'
        target.write_bytes(target.read_bytes() + b'\n')
        with self.assertRaisesRegex(ValueError, 'unexpected contents'):
            patch(self.source, self.destination, self.evidence)

    def test_installed_source_and_ancestors_cannot_be_destinations(self):
        for target in [self.source, self.source / 'child', self.base]:
            with self.subTest(target=target), self.assertRaisesRegex(ValueError, 'disjoint'):
                patch(self.source, target, self.evidence)


if __name__ == '__main__':
    unittest.main()
