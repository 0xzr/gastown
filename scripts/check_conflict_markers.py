#!/usr/bin/env python3
"""check_conflict_markers.py — detect stray Git conflict markers.

Ignores false positives inside Go raw string literals.

Usage:
    check_conflict_markers.py [path ...]

When invoked without arguments, the script uses ``git grep`` to find candidate
lines in the current repository.  When invoked with paths, it scans those files
directly (intended for tests).

Exit codes:
    0 — no conflict markers found
    1 — conflict markers detected (or an error occurred)
"""

from __future__ import annotations

import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable

# Git conflict marker prefixes. The separator is exactly seven '=' characters;
# the surrounding markers include the branch name after a space. Decorative
# runs like '===============' therefore do not match.
CONFLICT_RE = re.compile(r"^(<<<<<<< |=======$|>>>>>>> )")


def raw_string_lines(file_text: str) -> set[int]:
    """Return the 1-based line numbers whose first column is inside a Go raw
    string literal.

    The state machine tracks Go lexical states (line/block comments, runes,
    interpreted strings, and raw strings).  Raw strings cannot contain a backtick,
    so the state change on backticks is unambiguous once raw-string state is
    entered.
    """
    lines: set[int] = set()
    state: str = "normal"  # normal, line_comment, block_comment, rune, string, raw
    line_no = 1
    i = 0
    n = len(file_text)

    while i < n:
        ch = file_text[i]

        if ch == "\n":
            line_no += 1
            # Record whether the *start* of the next line is inside a raw string.
            if state == "raw":
                lines.add(line_no)
            i += 1
            continue

        if state == "line_comment":
            if ch == "\n":
                state = "normal"
            i += 1
            continue

        if state == "block_comment":
            if ch == "*" and i + 1 < n and file_text[i + 1] == "/":
                state = "normal"
                i += 2
                continue
            i += 1
            continue

        if state == "rune" or state == "string":
            if ch == "\\" and i + 1 < n:
                # Skip the escaped character.
                i += 2
                continue
            if state == "rune" and ch == "'":
                state = "normal"
            elif state == "string" and ch == '"':
                state = "normal"
            i += 1
            continue

        if state == "raw":
            if ch == "`":
                state = "normal"
            i += 1
            continue

        # normal state
        if ch == "/" and i + 1 < n:
            nxt = file_text[i + 1]
            if nxt == "/":
                state = "line_comment"
                i += 2
                continue
            if nxt == "*":
                state = "block_comment"
                i += 2
                continue
        elif ch == '"':
            state = "string"
        elif ch == "'":
            state = "rune"
        elif ch == "`":
            state = "raw"

        i += 1

    return lines


@dataclass(frozen=True)
class Marker:
    file: str
    line: int
    text: str


def _parse_git_grep(line: str) -> Marker | None:
    """Parse a ``git grep -n`` output line: file:lineno:text."""
    first_colon = line.find(":")
    if first_colon == -1:
        return None
    second_colon = line.find(":", first_colon + 1)
    if second_colon == -1:
        return None
    file_name = line[:first_colon]
    try:
        line_no = int(line[first_colon + 1 : second_colon])
    except ValueError:
        return None
    text = line[second_colon + 1 :]
    return Marker(file=file_name, line=line_no, text=text)


def _marker_prefix(text: str) -> str | None:
    """Return the matched conflict-marker prefix, or None."""
    m = CONFLICT_RE.match(text)
    if m:
        return m.group(0)
    return None


def _find_via_git_grep() -> list[Marker]:
    result = subprocess.run(
        [
            "git",
            "grep",
            "-n",
            "-E",
            r"^(<<<<<<< |=======$|>>>>>>> )",
            "--",
            ".",
        ],
        capture_output=True,
        text=True,
    )
    if result.returncode not in (0, 1):
        # 1 means no matches; anything else is a real error.
        print(
            f"git grep failed (exit {result.returncode}): {result.stderr.strip()}",
            file=sys.stderr,
        )
        raise SystemExit(1)

    markers: list[Marker] = []
    for line in result.stdout.splitlines():
        marker = _parse_git_grep(line)
        if marker is not None and _marker_prefix(marker.text) is not None:
            markers.append(marker)
    return markers


def filter_go_raw_string_markers(markers: Iterable[Marker]) -> list[Marker]:
    """Drop markers that fall inside Go raw string literals."""
    by_file: dict[str, set[int]] = {}
    for marker in markers:
        if marker.file.endswith(".go"):
            by_file.setdefault(marker.file, set()).add(marker.line)

    raw_lines_by_file: dict[str, set[int]] = {}
    for file_name, lines in by_file.items():
        path = Path(file_name)
        if not path.is_file():
            continue
        try:
            text = path.read_text(encoding="utf-8")
        except OSError:
            continue
        raw_lines = raw_string_lines(text)
        # raw_lines contains line numbers whose start is inside a raw string.
        # Keep only lines that were reported as markers.
        reported = raw_lines & lines
        if reported:
            raw_lines_by_file[file_name] = reported

    filtered: list[Marker] = []
    for marker in markers:
        if marker.file.endswith(".go") and marker.line in raw_lines_by_file.get(
            marker.file, set()
        ):
            continue
        filtered.append(marker)
    return filtered


def scan_files(paths: list[str]) -> list[Marker]:
    """Scan the supplied file paths and return any conflict markers found.

    For ``.go`` files, markers inside raw string literals are ignored.
    """
    markers: list[Marker] = []
    for path_str in paths:
        path = Path(path_str)
        if not path.is_file():
            continue
        text = path.read_text(encoding="utf-8")
        raw_lines = raw_string_lines(text) if path_str.endswith(".go") else set()
        for line_no, line in enumerate(text.splitlines(), start=1):
            if _marker_prefix(line) is not None and line_no not in raw_lines:
                markers.append(Marker(file=str(path), line=line_no, text=line))
    return markers


def print_markers(markers: Iterable[Marker]) -> None:
    for marker in markers:
        print(f"{marker.file}:{marker.line}:{marker.text}")


def main(argv: list[str] | None = None) -> int:
    args = argv if argv is not None else sys.argv[1:]

    if args:
        markers = scan_files(args)
    else:
        markers = _find_via_git_grep()

    markers = filter_go_raw_string_markers(markers)

    if markers:
        print_markers(markers)
        return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
