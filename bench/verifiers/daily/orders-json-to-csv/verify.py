import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import expect_equal, ok, read_csv, unchanged  # noqa: E402

TASK = Path(__file__).resolve().parent.name

unchanged(TASK, "orders.json")
header, rows = read_csv("orders.csv")
expect_equal(
    header,
    ["order_id", "customer_name", "customer_email", "item_count", "total_cents", "status"],
    "orders.csv header",
)
expected = [
    ["A-1039", "Ben Okafor", "ben@okafor.example", "1", "4599", "delivered"],
    ["A-1040", "Emma Novak", "emma.novak@example.com", "0", "0", "shipped"],
    ["A-1041", "David Cohen", "david.cohen@example.com", "9", "5691", "processing"],
    ["A-1042", "Ana Ruiz", "ana.ruiz@example.com", "3", "3399", "shipped"],
    ["A-1043", "Farid Haddad", "farid.h@example.com", "2", "5849", "delivered"],
]
actual = [[row[col] for col in header] for row in rows]
expect_equal(actual, expected, "orders.csv rows")
ok()
