import os
import subprocess
import tempfile
import unittest
from pathlib import Path


CHECK = Path(__file__).with_name("check_clean_source.sh").resolve()


class CleanSourceTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.env = {key: value for key, value in os.environ.items()
                    if not key.startswith("GIT_")}
        self.git("init", "--quiet")
        (self.root / "source.txt").write_text("Committed source.\n")
        (self.root / ".gitignore").write_text("bin/\n")
        self.git("add", ".")
        self.git("-c", "user.name=mura", "-c", "user.email=murakamikaze@validao.xyz",
                 "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "Fixture baseline")

    def git(self, *args):
        return subprocess.run(
            ["git", "-c", "core.hooksPath=/dev/null", *args], cwd=self.root,
            env=self.env, capture_output=True, text=True, check=True,
        )

    def check(self, cwd=None):
        return subprocess.run(
            ["bash", str(CHECK)], cwd=cwd or self.root, env=self.env,
            capture_output=True, text=True,
        )

    def assert_rejected(self, path):
        result = self.check()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(path, result.stderr)

    def test_clean_checkout_passes(self):
        self.assertEqual(self.check().returncode, 0)

    def test_unstaged_source_change_is_rejected(self):
        (self.root / "source.txt").write_text("Changed source.\n")
        self.assert_rejected("source.txt")

    def test_staged_source_change_is_rejected(self):
        (self.root / "source.txt").write_text("Changed source.\n")
        self.git("add", "source.txt")
        self.assert_rejected("source.txt")

    def test_deleted_source_is_rejected(self):
        (self.root / "source.txt").unlink()
        self.assert_rejected("source.txt")

    def test_untracked_file_is_rejected_even_when_git_hides_untracked_files(self):
        self.git("config", "status.showUntrackedFiles", "no")
        (self.root / "unexpected file.txt").write_text("Untracked file.\n")
        self.assert_rejected("unexpected file.txt")

    def test_untracked_python_bytecode_is_rejected(self):
        bytecode = self.root / ".github/scripts/__pycache__/release_notes.cpython.pyc"
        bytecode.parent.mkdir(parents=True)
        bytecode.write_bytes(b"bytecode fixture")
        self.assert_rejected("release_notes.cpython.pyc")

    def test_ignored_build_output_does_not_dirty_source(self):
        output = self.root / "bin/exporter"
        output.parent.mkdir()
        output.write_bytes(b"build fixture")
        self.assertEqual(self.check().returncode, 0)

    def test_git_failure_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            self.assertNotEqual(self.check(cwd=directory).returncode, 0)


if __name__ == "__main__":
    unittest.main()
