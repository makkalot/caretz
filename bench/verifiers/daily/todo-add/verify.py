import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import expect_equal, ok, require_file, seed_path  # noqa: E402

TASK = Path(__file__).resolve().parent.name


def lines(path):
    out = [line.rstrip() for line in path.read_text(encoding="utf-8").splitlines()]
    while out and not out[-1]:
        out.pop()
    return out


expected = lines(seed_path(TASK, "todo.md")) + ["- [ ] Call the plumber"]
expect_equal(lines(require_file("todo.md")), expected, "todo.md lines")
ok()
