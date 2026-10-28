I need the orders in `orders.json` as a spreadsheet. Please create `orders.csv` with one row per order and exactly these columns, in this order:

`order_id,customer_name,customer_email,item_count,total_cents,status`

- `item_count` is the total quantity across the order's items.
- `total_cents` is the sum of `qty * unit_price_cents` over the order's items.
- Leave out cancelled orders.
- Sort the rows by `order_id`.

Include a header row, and don't modify `orders.json`.
