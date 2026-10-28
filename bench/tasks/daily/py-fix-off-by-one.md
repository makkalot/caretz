Our order history pagination in `paginate.py` is broken. With 10 orders and 3 per page, the UI shows only 3 pages and the last order never appears. Page 1 also seems to skip the newest orders.

Please fix `paginate.py` so every item shows up exactly once across pages 1..N. Keep the function names and signatures the same.
