import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from release_notes import release_notes


class ReleaseNotesTests(unittest.TestCase):
    def test_extracts_only_the_exact_tag(self):
        changelog = """# Changelog

## [Unreleased]
- Future change.

## [v1.2.30] - 2026-09-27
- A different release.

## [v1.2.3] - 2026-09-26

### Fixed

- Correct a metric label.

## [v1.2.2] - 2026-09-25
- An earlier change.
"""
        self.assertEqual(release_notes(changelog, "v1.2.3"),
                         "### Fixed\n\n- Correct a metric label.\n")

    def test_preserves_upgrade_instructions_and_links(self):
        body = "### Breaking\n\nRename `old_metric` to `new_metric`.\n\n[Upgrade](https://example.com/UPGRADING.md)\n"
        self.assertEqual(release_notes("## [v2.0.0]\n" + body, "v2.0.0"), body)

    def test_rejects_missing_tag(self):
        with self.assertRaisesRegex(ValueError, "found 0"):
            release_notes("## [v1.2.30]\n- Change.\n", "v1.2.3")

    def test_rejects_duplicate_tag(self):
        with self.assertRaisesRegex(ValueError, "found 2"):
            release_notes("## [v1.0.0]\n- One.\n## [v1.0.0]\n- Two.\n", "v1.0.0")

    def test_rejects_empty_or_heading_only_notes(self):
        for body in ("", "\n", "\n### Fixed\n\n"):
            with self.subTest(body=body), self.assertRaisesRegex(ValueError, "no release notes"):
                release_notes("## [v1.0.0]\n" + body, "v1.0.0")

    def test_failed_extraction_preserves_existing_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            changelog, output = root / "CHANGELOG.md", root / "notes.md"
            changelog.write_text("## [Unreleased]\n")
            output.write_text("Existing notes.\n")
            result = subprocess.run(
                [sys.executable, str(Path(__file__).with_name("release_notes.py")), "v1.0.0",
                 "--changelog", str(changelog), "--output", str(output)],
                capture_output=True, text=True,
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(output.read_text(), "Existing notes.\n")


if __name__ == "__main__":
    unittest.main()
