I'm planning a trip and want to check it against my budget of 150 EUR per day. The planned expenses are in `itinerary.md` and the exchange rates are in `rates.json`.

Rules:

- Convert every expense to EUR with the rates in `rates.json`.
- Don't count anything marked as already prepaid.
- For expenses split with someone, only count my share.
- A day is over budget when its total is more than 150 EUR.

Create `budget.json` in this shape:

```json
{
  "days": [
    {"date": "2024-06-03", "total_eur": 123.45, "over_budget": false}
  ],
  "total_eur": 1234.56,
  "over_budget_dates": ["2024-06-05"]
}
```

- `days` lists every day in date order, with `total_eur` rounded to 2 decimals.
- `total_eur` is the sum of the day totals, rounded to 2 decimals.
- `over_budget_dates` lists the dates of over-budget days, in date order.

Don't modify the input files.
