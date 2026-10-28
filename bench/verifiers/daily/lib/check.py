"""Shared helpers for daily-task verifiers.

Verifiers run with the completed task workspace as the current directory and
BENCH_ROOT pointing at the bench config root. They grade only files the model
wrote, never its chat reply: question tasks ask the model to save the answer to
answer.txt or answer.json.
"""

import contextlib
import csv
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import NoReturn


def fail(msg) -> NoReturn:
    print(f"FAIL: {msg}")
    sys.exit(1)


def ok(msg="all checks passed") -> NoReturn:
    print(f"PASS: {msg}")
    sys.exit(0)


def bench_root():
    root = os.environ.get("BENCH_ROOT")
    if not root:
        fail("BENCH_ROOT is not set")
    return Path(root)


def seed_path(task, rel):
    """Path of rel inside the task's pristine seed workspace."""
    return bench_root() / "workspaces" / "daily" / task / rel


def hidden_path(task, rel):
    """Path of rel inside the task's verifier directory (hidden from the model)."""
    return bench_root() / "verifiers" / "daily" / task / rel


def unchanged(task, *rels):
    """Fail unless each workspace file still matches the seed workspace."""
    for rel in rels:
        path = Path(rel)
        if not path.is_file():
            fail(f"input {rel} is missing")
        if path.read_bytes() != seed_path(task, rel).read_bytes():
            fail(f"input {rel} was modified")


@contextlib.contextmanager
def workspace_copy(task, hidden=None):
    """Copy the workspace to a temp dir and add hidden verifier files.

    hidden maps a path under verifiers/daily/<task>/ to its destination in the
    copy, so hidden tests never land in the graded workspace.
    """
    with tempfile.TemporaryDirectory() as tmp:
        work = Path(tmp) / "work"
        shutil.copytree(".", work, symlinks=True)
        for src, dst in (hidden or {}).items():
            shutil.copy(hidden_path(task, src), work / dst)
        yield work


def run(cmd, cwd, timeout=120, env=None, input=None):
    """Run cmd and return the completed process; fail only on timeout.

    env entries override the inherited environment; a None value unsets it.
    """
    full_env = dict(os.environ)
    for key, value in (env or {}).items():
        if value is None:
            full_env.pop(key, None)
        else:
            full_env[key] = value
    try:
        return subprocess.run(
            cmd,
            cwd=cwd,
            capture_output=True,
            text=True,
            timeout=timeout,
            env=full_env,
            input=input,
        )
    except subprocess.TimeoutExpired:
        fail(f"{' '.join(map(str, cmd))} timed out after {timeout}s")
    except FileNotFoundError as exc:
        fail(f"cannot run {cmd[0]}: {exc}")


def run_ok(cmd, cwd, **kwargs):
    """Run cmd and fail with its output unless it exits 0."""
    proc = run(cmd, cwd, **kwargs)
    if proc.returncode != 0:
        fail(f"{' '.join(map(str, cmd))} exited {proc.returncode}\n{proc.stdout}{proc.stderr}")
    return proc


def require_file(rel):
    path = Path(rel)
    if not path.is_file():
        fail(f"{rel} was not created")
    return path


def answer_text(rel="answer.txt"):
    text = require_file(rel).read_text(encoding="utf-8", errors="replace").strip()
    if not text:
        fail(f"{rel} is empty")
    return text


_NUMBER = re.compile(r"-?\d[\d,]*(?:\.\d+)?")


def numbers_in(text):
    """Numbers in text, ignoring currency symbols and thousands separators."""
    return [float(m.replace(",", "")) for m in _NUMBER.findall(text)]


def load_json(rel):
    path = require_file(rel)
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        fail(f"{rel} is not valid JSON: {exc}")


def read_csv(rel):
    """Rows of a CSV file as (header, list of dicts) with whitespace trimmed."""
    path = require_file(rel)
    with path.open(newline="", encoding="utf-8") as f:
        reader = csv.reader(f)
        rows = [row for row in reader if any(cell.strip() for cell in row)]
    if not rows:
        fail(f"{rel} is empty")
    header = [cell.strip() for cell in rows[0]]
    records = []
    for i, row in enumerate(rows[1:], start=2):
        if len(row) != len(header):
            fail(f"{rel} line {i} has {len(row)} columns, want {len(header)}")
        records.append({k: v.strip() for k, v in zip(header, row)})
    return header, records


def expect_equal(actual, expected, what):
    if actual != expected:
        fail(f"{what} mismatch\n  expected: {expected!r}\n  actual:   {actual!r}")


def same(actual, expected):
    """Deep equality where booleans and null must match exactly (1 is not True)
    but 30 and 30.0 are the same number."""
    if isinstance(expected, bool) or expected is None:
        return actual is expected
    if isinstance(expected, (int, float)):
        return isinstance(actual, (int, float)) and not isinstance(actual, bool) and actual == expected
    if isinstance(expected, dict):
        return (
            isinstance(actual, dict)
            and sorted(actual) == sorted(expected)
            and all(same(actual[k], v) for k, v in expected.items())
        )
    if isinstance(expected, list):
        return (
            isinstance(actual, list)
            and len(actual) == len(expected)
            and all(same(a, e) for a, e in zip(actual, expected))
        )
    return actual == expected


def close(a, b, tol=0.011):
    try:
        return abs(float(a) - float(b)) <= tol
    except (TypeError, ValueError):
        return False
