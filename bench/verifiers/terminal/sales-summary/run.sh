#!/usr/bin/env bash
set -euo pipefail

python3 - <<'PY'
import json
from pathlib import Path

report_path = Path("report.json")
assert report_path.exists(), "report.json was not created"

with report_path.open() as f:
    report = json.load(f)

expected = {
    "total_orders": 6,
    "total_units": 14,
    "total_revenue_cents": 22072,
    "revenue_by_region_cents": {
        "east": 13500,
        "north": 2050,
        "south": 2625,
        "west": 3897,
    },
    "top_sku_by_revenue": "B200",
}

assert report == expected, f"report.json mismatch:\nexpected={expected!r}\nactual={report!r}"
PY
