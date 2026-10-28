We need solve this terminal benchmark task in the repository at the current working directory.

Read `data/orders.csv` and create `report.json` in the workspace root.

The report must be valid JSON with exactly these top-level fields:

- `total_orders`: number of order rows
- `total_units`: sum of the `quantity` column
- `total_revenue_cents`: sum of `quantity * unit_price_cents`
- `revenue_by_region_cents`: object mapping each region to its total revenue in cents
- `top_sku_by_revenue`: SKU with the highest total revenue in cents

Do not modify the input CSV.
