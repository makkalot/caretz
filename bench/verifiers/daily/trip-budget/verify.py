import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import close, expect_equal, fail, load_json, ok, unchanged  # noqa: E402

TASK = Path(__file__).resolve().parent.name

# Prepaid hotels are excluded; the Day 2 dinner and Day 4 taxi count at half.
expected_days = [
    ("2024-06-03", 121.16, False),
    ("2024-06-04", 136.24, False),
    ("2024-06-05", 186.73, True),
    ("2024-06-06", 136.77, False),
    ("2024-06-07", 465.06, True),
]

unchanged(TASK, "itinerary.md", "rates.json")
budget = load_json("budget.json")
if not isinstance(budget, dict) or not isinstance(budget.get("days"), list):
    fail("budget.json must be an object with a days list")
days = budget["days"]
expect_equal([d.get("date") for d in days], [d for d, _, _ in expected_days], "day dates")
for day, (date, total, over) in zip(days, expected_days):
    if not close(day.get("total_eur"), total, tol=0.02):
        fail(f"{date} total_eur {day.get('total_eur')!r} != {total}")
    expect_equal(day.get("over_budget"), over, f"{date} over_budget")
if not close(budget.get("total_eur"), 1045.96, tol=0.03):
    fail(f"total_eur {budget.get('total_eur')!r} != 1045.96")
expect_equal(budget.get("over_budget_dates"), ["2024-06-05", "2024-06-07"], "over_budget_dates")
ok()
