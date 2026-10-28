import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import ok, require_file, run_ok, workspace_copy  # noqa: E402

TASK = Path(__file__).resolve().parent.name

require_file("duration.py")
with workspace_copy(TASK, {"hidden/test_hidden.py": "test_hidden.py"}) as work:
    run_ok([sys.executable, "-m", "unittest", "-v", "test_hidden"], work)
ok()
