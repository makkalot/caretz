import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import expect_equal, fail, ok, require_file, unchanged  # noqa: E402

TASK = Path(__file__).resolve().parent.name
LINE = re.compile(r"^- \[ \] (.+?) \(owner: ([^,()]+), due: (\d{4}-\d{2}-\d{2})\)\s*$")

# (owner, due) -> a keyword the task text must mention.
expected = {
    ("Tom", "2024-03-12"): "login",
    ("Priya", "2024-03-14"): "budget",
    ("Marco", "2024-03-15"): "venue",
    ("Marco", "2024-03-18"): "onboarding",
    ("Lena", "2024-03-20"): "survey",
}

unchanged(TASK, "notes.md")
items = {}
for line in require_file("actions.md").read_text(encoding="utf-8").splitlines():
    if not line.strip():
        continue
    match = LINE.match(line.strip())
    if not match:
        fail(f"line does not match the required format: {line!r}")
    task, owner, due = match.groups()
    key = (owner.strip(), due)
    if key in items:
        fail(f"duplicate action item for {key}")
    items[key] = task

expect_equal(sorted(items), sorted(expected), "(owner, due) pairs")
for key, keyword in expected.items():
    if keyword not in items[key].lower():
        fail(f"item for {key} should mention {keyword!r}: {items[key]!r}")
ok()
