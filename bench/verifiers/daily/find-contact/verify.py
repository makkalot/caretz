import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import answer_text, fail, ok, unchanged  # noqa: E402

TASK = Path(__file__).resolve().parent.name

unchanged(TASK, "contacts.csv")
digits = re.sub(r"\D", "", answer_text())
if digits not in ("14155550182", "4155550182"):
    fail(f"answer.txt digits {digits!r} are not Dana Lee's number +1 415-555-0182")
ok()
