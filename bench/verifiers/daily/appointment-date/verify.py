import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import expect_equal, fail, load_json, ok  # noqa: E402

answer = load_json("answer.json")
if not isinstance(answer, dict):
    fail("answer.json must be an object with date and time")
expect_equal(str(answer.get("date", "")).strip(), "2024-05-16", "date")
expect_equal(str(answer.get("time", "")).strip(), "14:30", "time")
ok()
