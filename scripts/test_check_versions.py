#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Tests of scripts/check-versions.sh: it runs on a copy of the repository, as
it is and with one pin changed in one place."""

import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def own_dockerfile(root):
    """The Dockerfile of the one own stack (own/<stack>/Dockerfile)."""
    found = sorted(root.glob("own/*/Dockerfile"))
    if len(found) != 1:
        raise AssertionError("want one own/*/Dockerfile, found %s" % found)
    return found[0]


class CheckVersions(unittest.TestCase):

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name) / "repo"
        shutil.copytree(ROOT, self.root, ignore=shutil.ignore_patterns(".git", "reports", "node_modules", "target", "__pycache__"))

    def tearDown(self):
        self.tmp.cleanup()

    def run_check(self):
        p = subprocess.run(["bash", str(self.root / "scripts" / "check-versions.sh")], capture_output=True, text=True)
        return p.returncode, p.stdout + p.stderr

    def edit(self, path, old, new):
        path = self.root / path
        text = path.read_text()
        self.assertIn(old, text, path)
        path.write_text(text.replace(old, new, 1))

    def assert_fails_with(self, needle):
        code, out = self.run_check()
        self.assertEqual(code, 1, out)
        self.assertIn(needle, out)

    def test_the_repository_passes(self):
        code, out = self.run_check()
        self.assertEqual(code, 0, out)

    def test_a_missing_revision_fails(self):
        self.edit("versions.yaml", '    revision: "902f5e0d4ac041778ceb84376d4f85c2c8570425"', '    upstream_tag: "v1.0.21"')
        self.assert_fails_with("FAIL tahu: revision '' is not a commit")

    def test_a_revision_that_is_not_a_commit_fails(self):
        self.edit("versions.yaml", '    revision: "957b5b844e3b229f8896cb3d786745e6ed196968"', '    revision: "v0.6.1"')
        self.assert_fails_with("FAIL pysparkplug: revision 'v0.6.1' is not a commit")

    def test_a_missing_lock_fails(self):
        self.edit("versions.yaml", '    lock: "adapters/srad/Cargo.lock"', '    lock: "adapters/srad/Cargo.lock.old"')
        self.assert_fails_with("FAIL srad: lock 'adapters/srad/Cargo.lock.old' is not a file of this repository")

    def test_a_lock_without_the_version_fails(self):
        self.edit("adapters/pysparkplug/requirements.txt", "pysparkplug==0.6.1", "pysparkplug==0.6.2")
        self.assert_fails_with("FAIL pysparkplug: adapters/pysparkplug/requirements.txt does not pin version 0.6.1")

    def test_an_own_stack_digest_versions_yaml_does_not_list_fails(self):
        dockerfile = own_dockerfile(self.root)
        text = dockerfile.read_text()
        digest = re.search(r"^ARG GOLANG_DIGEST=(sha256:[0-9a-f]{64})$", text, re.M).group(1)
        dockerfile.write_text(text.replace(digest, "sha256:" + "1" * 64))
        self.assert_fails_with("is not in own_implementations.")

    def test_an_own_stack_digest_changed_in_versions_yaml_only_fails(self):
        text = (self.root / "versions.yaml").read_text()
        own = text[text.index("own_implementations:"):]
        digest = re.search(r'golang: "[^"@]+@(sha256:[0-9a-f]{64})"', own).group(1)
        (self.root / "versions.yaml").write_text(text[:text.index("own_implementations:")] + own.replace(digest, "sha256:" + "2" * 64))
        self.assert_fails_with("is not a base image of own/")

    def test_an_own_stack_without_an_entry_fails(self):
        dockerfile = own_dockerfile(self.root)
        build = "/".join(dockerfile.relative_to(self.root).parts[:2])
        self.edit("versions.yaml", '    build: "%s"' % build, '    build: "own/elsewhere"')
        self.assert_fails_with("no own_implementations entry has build: %s" % build)


if __name__ == "__main__":
    unittest.main()
