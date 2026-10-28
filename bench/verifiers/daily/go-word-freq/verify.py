import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import ok, require_file, run_ok, workspace_copy  # noqa: E402

TASK = Path(__file__).resolve().parent.name
# Keep go from downloading toolchains or joining a parent go.work.
GO_ENV = {"GOWORK": "off", "GOTOOLCHAIN": "local", "GOFLAGS": "-mod=mod"}

require_file("go.mod")
require_file("wordfreq.go")
with workspace_copy(TASK, {"hidden/wordfreq_test.go.txt": "hidden_wordfreq_test.go"}) as work:
    run_ok(["go", "vet", "./..."], work, env=GO_ENV)
    run_ok(["go", "test", "-count=1", "-run", "TestHidden", "./..."], work, env=GO_ENV)
ok()
