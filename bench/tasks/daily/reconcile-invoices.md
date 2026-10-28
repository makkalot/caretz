I need to reconcile my invoices against the payments I've received. Invoices are in `invoices.csv` and the bank payments are in `payments.csv`.

Matching rules:

- A payment belongs to an invoice when its `reference` contains that invoice's number (for example `1003` for `INV-1003`), however it's written.
- An invoice can be paid in several payments.
- If a payment's reference doesn't contain the number of one of my invoices, it is unmatched. Don't guess from customer names or amounts.

Create `reconciliation.json` in this shape:

```json
{
  "invoices": {
    "INV-1001": {"paid_cents": 0, "balance_cents": 0, "status": "unpaid"}
  },
  "unmatched_payments": ["P-000"]
}
```

- Include every invoice in `invoices`.
- `paid_cents` is the total matched payments in cents.
- `balance_cents` is the invoice amount minus `paid_cents`, in cents. It is negative when overpaid.
- `status` is one of `paid` (paid exactly), `partial`, `unpaid` (nothing paid), or `overpaid`.
- `unmatched_payments` lists the unmatched payment IDs, sorted.

Don't modify the input files.
