import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import fail, load_json, ok, same, unchanged  # noqa: E402

TASK = Path(__file__).resolve().parent.name

expected = {
    "user": {"name": "makka"},
    "appearance": {"theme": "dark", "font_size_px": 16, "language": "en"},
    "editor": {
        "tab_width": 4,
        "show_whitespace": False,
        "word_wrap": False,
        "autosave": {"enabled": True, "interval_seconds": 300},
    },
    "network": {"timeout_seconds": 30, "retries": 3, "proxy": None, "offline_mode": False},
}

unchanged(TASK, "settings.ini", "MIGRATION.md")
actual = load_json("settings.json")
if not same(actual, expected):
    fail(f"settings.json mismatch\n  expected: {expected!r}\n  actual:   {actual!r}")
ok()
