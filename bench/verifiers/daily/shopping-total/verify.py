import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import answer_text, close, fail, numbers_in, ok, unchanged  # noqa: E402

TASK = Path(__file__).resolve().parent.name

unchanged(TASK, "receipt.txt")
numbers = numbers_in(answer_text())
if len(numbers) != 1:
    fail(f"answer.txt should hold just the amount, found numbers {numbers}")
if not close(numbers[0], 55.58, tol=0.001):
    fail(f"total {numbers[0]} != 55.58")
ok()
