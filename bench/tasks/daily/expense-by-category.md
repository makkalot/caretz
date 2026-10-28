I want to see where my money went in April. My bank export is `statement.csv` and my category rules are in `categories.md`.

Only count spending, which means the negative amounts. Ignore money coming in, including salary and refunds.

Create `summary.json` with exactly these fields:

- `total_spent_cents`: total spending as a positive whole number of cents
- `by_category_cents`: object mapping each category (`groceries`, `dining`, `transport`, `utilities`, `other`) to its spending in cents, as positive whole numbers
- `largest_category`: the category I spent the most on

Don't modify the input files.
