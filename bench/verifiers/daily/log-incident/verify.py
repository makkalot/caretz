import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import expect_equal, fail, ok, require_file, unchanged  # noqa: E402

TASK = Path(__file__).resolve().parent.name
FIELD = re.compile(r"^[\s>*-]*(start|end|errors|top_service)\s*:\s*(.+?)\s*$", re.IGNORECASE)

# Values for app.log as produced by generate_log.py.
expected = {
    "start": "2024-04-02T12:53:10Z",
    "end": "2024-04-02T12:57:51Z",
    "errors": "26",
    "top_service": "payments",
}

unchanged(TASK, "app.log")
fields = {}
for line in require_file("incident.md").read_text(encoding="utf-8").splitlines():
    match = FIELD.match(line.replace("**", ""))
    if match and match.group(1).lower() not in fields:
        fields[match.group(1).lower()] = match.group(2).strip("`'\" []")
missing = [key for key in expected if key not in fields]
if missing:
    fail(f"incident.md is missing fields: {missing}")
expect_equal({key: fields[key] for key in expected}, expected, "incident fields")
ok()
