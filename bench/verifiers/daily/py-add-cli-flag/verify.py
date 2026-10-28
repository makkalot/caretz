import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import expect_equal, fail, ok, require_file, run, workspace_copy  # noqa: E402

TASK = Path(__file__).resolve().parent.name
WORDS = ["a", "and", "apple", "cat", "cat's", "hat", "the", "zebra"]

cases = [
    ([], WORDS),
    (["--reverse"], WORDS[::-1]),
    (["--limit", "3"], WORDS[:3]),
    (["--reverse", "--limit", "2"], ["zebra", "the"]),
    (["--limit", "100"], WORDS),
]

require_file("wordlist.py")
with workspace_copy(TASK, {"hidden/words.txt": "words.txt"}) as work:
    for args, want in cases:
        for argv in (args + ["words.txt"], ["words.txt"] + args):
            proc = run([sys.executable, "wordlist.py", *argv], work)
            if proc.returncode != 0:
                fail(f"wordlist.py {' '.join(argv)} exited {proc.returncode}\n{proc.stderr}")
            expect_equal(proc.stdout.split(), want, f"output of wordlist.py {' '.join(argv)}")
    for bad in ("0", "-1", "abc", "2.5"):
        proc = run([sys.executable, "wordlist.py", "words.txt", "--limit", bad], work)
        if proc.returncode != 2:
            fail(f"--limit {bad} should be rejected with a usage error (exit 2), got exit {proc.returncode}")
        if proc.stdout.strip():
            fail(f"--limit {bad} should not print words, got {proc.stdout!r}")
ok()
