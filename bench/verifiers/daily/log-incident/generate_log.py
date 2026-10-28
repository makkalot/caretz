"""Regenerate the log-incident seed log deterministically.

Usage: python3 -I generate_log.py bench/workspaces/daily/log-incident/app.log

The log has isolated errors, a decoy cluster of 3 errors within a minute, and
one outage: a run of ERROR lines no more than 60 seconds apart. If you change
this script, update the expected values in verify.py and solution/incident.md.
"""

import random
import sys
from datetime import datetime, timedelta, timezone
from pathlib import Path

SERVICES = ["checkout", "payments", "search", "auth", "inventory"]
INFO = [
    "request ok latency_ms={ms}",
    "cache hit key=user:{n}",
    "request ok latency_ms={ms}",
    "health check passed",
    "request ok latency_ms={ms}",
]
WARN = ["slow request latency_ms={ms}", "retrying upstream call attempt=2"]
ERRORS = {
    "payments": "upstream timeout after 5000ms order={n}",
    "checkout": "failed to reserve cart cart={n}",
    "auth": "token validation failed user={n}",
    "inventory": "stock lookup failed sku=SKU-{n}",
    "search": "index shard unavailable shard={n}",
}
ISOLATED = {150: "search", 420: "auth", 700: "inventory", 1600: "checkout", 1850: "payments"}
DECOY = {1300: "inventory", 1304: "inventory", 1309: "auth"}
OUTAGE_START, OUTAGE_END = 905, 980
OUTAGE_CYCLE = ["payments", "payments", "checkout", "payments", "auth", "payments", "checkout"]


def main(path):
    rng = random.Random(7)
    ts = datetime(2024, 4, 2, 12, 0, 0, tzinfo=timezone.utc)
    outage_i = 0
    lines = []
    for i in range(2000):
        ts += timedelta(seconds=rng.randint(1, 6))
        level, service = "INFO", rng.choice(SERVICES)
        msg = rng.choice(INFO)
        if i in ISOLATED:
            level, service = "ERROR", ISOLATED[i]
        elif i in DECOY:
            level, service = "ERROR", DECOY[i]
        elif OUTAGE_START <= i <= OUTAGE_END and (i - OUTAGE_START) % 3 == 0:
            level, service = "ERROR", OUTAGE_CYCLE[outage_i % len(OUTAGE_CYCLE)]
            outage_i += 1
        elif rng.random() < 0.05:
            level, msg = "WARN", rng.choice(WARN)
        if level == "ERROR":
            msg = ERRORS[service]
        text = msg.format(ms=rng.randint(20, 900), n=rng.randint(1000, 9999))
        lines.append(f"{ts.strftime('%Y-%m-%dT%H:%M:%SZ')} {level:<5} [{service}] {text}")
    out = Path(path)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text("\n".join(lines) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main(sys.argv[1])
