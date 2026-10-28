import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import expect_equal, load_json, ok, unchanged  # noqa: E402

TASK = Path(__file__).resolve().parent.name

unchanged(TASK, "statement.csv", "categories.md")
expected = {
    "total_spent_cents": 60120,
    "by_category_cents": {
        "groceries": 19576,
        "dining": 7670,
        "transport": 11230,
        "utilities": 15944,
        "other": 5700,
    },
    "largest_category": "groceries",
}
expect_equal(load_json("summary.json"), expected, "summary.json")
ok()
