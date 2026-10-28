import sys
from collections import Counter
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import expect_equal, fail, ok, read_csv, unchanged  # noqa: E402

TASK = Path(__file__).resolve().parent.name
DAYS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"]
AVAILABLE = {
    "Alex": {"Wed", "Thu", "Fri", "Sat", "Sun"},
    "Bea": {"Mon", "Tue", "Fri", "Sat", "Sun"},
    "Chen": {"Mon", "Tue", "Wed", "Thu", "Fri"},
    "Dee": {"Mon", "Wed", "Thu", "Sat"},
}
SLOTS = [(day, "dishes") for day in DAYS] + [("Mon", "trash"), ("Thu", "trash"), ("Sat", "vacuum")]

unchanged(TASK, "household.md")
header, rows = read_csv("schedule.csv")
expect_equal(header, ["day", "chore", "person"], "schedule.csv header")

assigned = {}
for row in rows:
    slot = (row["day"], row["chore"])
    if slot not in SLOTS:
        fail(f"unexpected chore row {row}")
    if slot in assigned:
        fail(f"{slot} is assigned more than once")
    person = row["person"]
    if person not in AVAILABLE:
        fail(f"unknown person {person!r}")
    if row["day"] not in AVAILABLE[person]:
        fail(f"{person} is not home on {row['day']} (rule 1)")
    assigned[slot] = person
missing = [slot for slot in SLOTS if slot not in assigned]
if missing:
    fail(f"chores without anyone assigned: {missing}")

for today, tomorrow in zip(DAYS, DAYS[1:]):
    if assigned[(today, "dishes")] == assigned[(tomorrow, "dishes")]:
        fail(f"{assigned[(today, 'dishes')]} does dishes on {today} and {tomorrow} (rule 2)")
counts = Counter(assigned.values())
for person in AVAILABLE:
    if not 2 <= counts[person] <= 3:
        fail(f"{person} has {counts[person]} chores, want 2 or 3 (rule 3)")
if assigned[("Sat", "vacuum")] == "Chen":
    fail("Chen vacuums (rule 4)")
per_day = Counter((day, person) for (day, _), person in assigned.items())
for (day, person), n in per_day.items():
    if n > 1:
        fail(f"{person} has {n} chores on {day} (rule 5)")
ok()
