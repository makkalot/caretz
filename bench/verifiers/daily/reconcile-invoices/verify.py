import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "lib"))
from check import expect_equal, fail, load_json, ok, unchanged  # noqa: E402

TASK = Path(__file__).resolve().parent.name


def inv(paid, balance, status):
    return {"paid_cents": paid, "balance_cents": balance, "status": status}


expected = {
    "invoices": {
        "INV-1001": inv(120000, 0, "paid"),
        "INV-1002": inv(45000, 0, "paid"),
        "INV-1003": inv(50000, 48000, "partial"),
        "INV-1004": inv(33000, -3000, "overpaid"),
        "INV-1005": inv(0, 64000, "unpaid"),
        "INV-1006": inv(21550, 0, "paid"),
        "INV-1007": inv(125000, 25000, "partial"),
        "INV-1008": inv(0, 8990, "unpaid"),
    },
    "unmatched_payments": ["P-508", "P-510", "P-511"],
}

unchanged(TASK, "invoices.csv", "payments.csv")
actual = load_json("reconciliation.json")
if not isinstance(actual, dict) or not isinstance(actual.get("invoices"), dict):
    fail("reconciliation.json must be an object with an invoices object")
expect_equal(sorted(actual["invoices"]), sorted(expected["invoices"]), "invoice ids")
for invoice_id, want in expected["invoices"].items():
    expect_equal(actual["invoices"][invoice_id], want, invoice_id)
expect_equal(actual.get("unmatched_payments"), expected["unmatched_payments"], "unmatched_payments")
ok()
