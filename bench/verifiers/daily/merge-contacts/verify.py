import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import expect_equal, ok, read_csv, unchanged  # noqa: E402

TASK = Path(__file__).resolve().parent.name

unchanged(TASK, "phone_contacts.csv", "work_contacts.csv")
header, rows = read_csv("contacts.csv")
expect_equal(header, ["name", "email", "phone", "updated"], "contacts.csv header")
expected = [
    ["Ana Ruiz", "ana.ruiz@example.com", "+34 600 999 888", "2024-03-05"],
    ["Ben Okafor", "ben@okafor.example", "+44 7700 900111", "2024-02-10"],
    ["Carla Mendes", "carla.m@example.com", "+351 912 000 333", "2023-06-15"],
    ["David Cohen", "david.cohen@example.com", "+972 50 555 0101", "2024-01-20"],
    ["Emma Novak-Berg", "emma.novak@example.com", "+420 601 444 555", "2024-04-11"],
    ["Farid Haddad", "farid.h@example.com", "+971 50 123 4567", "2024-02-28"],
    ["George Lee", "george@lee.example", "+1 206 555 0110", "2023-10-10"],
]
actual = [[row[col] for col in header] for row in rows]
expect_equal(actual, expected, "contacts.csv rows")
ok()
