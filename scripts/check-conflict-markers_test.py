#!/usr/bin/env python3
"""Unit tests for scripts/check_conflict_markers.py."""

from __future__ import annotations

import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
import check_conflict_markers as ccm


def norm_marker(marker: ccm.Marker) -> ccm.Marker:
    """Normalise a marker so tests do not depend on absolute temp paths."""
    return ccm.Marker(
        file=os.path.basename(marker.file), line=marker.line, text=marker.text
    )


class RawStringLineTests(unittest.TestCase):
    """Tests for raw_string_lines()."""

    def test_line_outside_raw_string_is_not_flagged(self) -> None:
        text = 'x := "not raw"\n'
        self.assertEqual(ccm.raw_string_lines(text), set())

    def test_single_line_raw_string(self) -> None:
        text = "x := `raw`\n"
        self.assertEqual(ccm.raw_string_lines(text), set())

    def test_multiline_raw_string_records_inner_lines(self) -> None:
        text = "x := `\nline one\n=======\nline three\n`\n"
        # Line 1 is the opening line (normal state at start).
        # Lines 2, 3, 4, 5 start inside the raw string.
        self.assertEqual(ccm.raw_string_lines(text), {2, 3, 4, 5})

    def test_decorative_equals_inside_raw_string_is_ignored(self) -> None:
        text = """body := `
===============
header
===============
`
"""
        self.assertEqual(ccm.raw_string_lines(text), {2, 3, 4, 5})

    def test_interpreted_string_does_not_toggle_raw_state(self) -> None:
        text = 'x := "foo`bar"\n=======\n'
        self.assertEqual(ccm.raw_string_lines(text), set())

    def test_rune_does_not_toggle_raw_state(self) -> None:
        text = "x := '`'\n=======\n"
        self.assertEqual(ccm.raw_string_lines(text), set())

    def test_line_comment_does_not_toggle_raw_state(self) -> None:
        text = "// `\n=======\n"
        self.assertEqual(ccm.raw_string_lines(text), set())

    def test_block_comment_does_not_toggle_raw_state(self) -> None:
        text = "/* ` */\n=======\n"
        self.assertEqual(ccm.raw_string_lines(text), set())

    def test_escaped_quote_in_string(self) -> None:
        text = 'x := "foo\\"`bar"\n=======\n'
        self.assertEqual(ccm.raw_string_lines(text), set())

    def test_two_raw_strings(self) -> None:
        text = "a := `\ninside one\n`\nb := `\ninside two\n`\n"
        self.assertEqual(ccm.raw_string_lines(text), {2, 3, 5, 6})


class MarkerPrefixTests(unittest.TestCase):
    """Tests for _marker_prefix()."""

    def test_left_marker(self) -> None:
        self.assertEqual(ccm._marker_prefix("<<<<<<< branch"), "<<<<<<< ")

    def test_right_marker(self) -> None:
        self.assertEqual(ccm._marker_prefix(">>>>>>> branch"), ">>>>>>> ")

    def test_separator(self) -> None:
        self.assertEqual(ccm._marker_prefix("======="), "=======")

    def test_multiple_equals_is_not_separator(self) -> None:
        self.assertIsNone(ccm._marker_prefix("==============="))

    def test_prefix_with_fewer_equals(self) -> None:
        self.assertIsNone(ccm._marker_prefix("======"))


class ScanFilesTests(unittest.TestCase):
    """Tests for scan_files()."""

    def test_plain_marker_detected(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / "a.txt"
            path.write_text("<<<<<<< branch\n")
            markers = ccm.scan_files([str(path)])
            self.assertEqual(len(markers), 1)
            self.assertEqual(
                norm_marker(markers[0]), ccm.Marker("a.txt", 1, "<<<<<<< branch")
            )

    def test_marker_inside_go_raw_string_ignored(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / "bad.go"
            path.write_text("body := `\n=======\n`\n")
            markers = ccm.scan_files([str(path)])
            self.assertEqual(markers, [])

    def test_marker_outside_go_raw_string_detected(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / "bad.go"
            path.write_text("body := `ok`\n=======\n")
            markers = ccm.scan_files([str(path)])
            self.assertEqual(len(markers), 1)

    def test_decoration_equals_in_raw_string_ignored(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / "mail.go"
            path.write_text(
                "msg := `\n===============\nsome body\n===============\n`\n"
            )
            markers = ccm.scan_files([str(path)])
            self.assertEqual(markers, [])

    def test_exact_separator_in_raw_string_ignored(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            path = Path(tmpdir) / "mail.go"
            path.write_text("msg := `\n=======\n`\n")
            markers = ccm.scan_files([str(path)])
            self.assertEqual(markers, [])


class GitGrepIntegrationTests(unittest.TestCase):
    """Integration tests that exercise the full CLI in a temporary git repo."""

    def _run_in_repo(self, tmpdir: str, *args: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            args,
            cwd=tmpdir,
            capture_output=True,
            text=True,
            env={**os.environ, "PYTHONPATH": str(Path(__file__).parent)},
        )

    def test_cli_finds_real_conflict_marker(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            self._run_in_repo(tmpdir, "git", "init")
            self._run_in_repo(tmpdir, "git", "config", "user.email", "test@example.com")
            self._run_in_repo(tmpdir, "git", "config", "user.name", "Test")
            Path(tmpdir, "a.txt").write_text("<<<<<<< branch\n")
            self._run_in_repo(tmpdir, "git", "add", ".")
            self._run_in_repo(tmpdir, "git", "commit", "-m", "init")
            result = subprocess.run(
                [str(Path(__file__).parent / "check_conflict_markers.py")],
                cwd=tmpdir,
                capture_output=True,
                text=True,
            )
            self.assertEqual(result.returncode, 1)
            self.assertIn("a.txt:1:<<<<<<< branch", result.stdout)

    def test_cli_ignores_raw_string_decoration_in_go(self) -> None:
        with tempfile.TemporaryDirectory() as tmpdir:
            self._run_in_repo(tmpdir, "git", "init")
            self._run_in_repo(tmpdir, "git", "config", "user.email", "test@example.com")
            self._run_in_repo(tmpdir, "git", "config", "user.name", "Test")
            Path(tmpdir, "mail.go").write_text("body := `\n===============\n`\n")
            self._run_in_repo(tmpdir, "git", "add", ".")
            self._run_in_repo(tmpdir, "git", "commit", "-m", "init")
            result = subprocess.run(
                [str(Path(__file__).parent / "check_conflict_markers.py")],
                cwd=tmpdir,
                capture_output=True,
                text=True,
            )
            self.assertEqual(result.returncode, 0)
            self.assertEqual(result.stdout.strip(), "")


if __name__ == "__main__":
    unittest.main()
