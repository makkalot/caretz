"""Parse human-friendly durations for the reminder CLI."""

import re

_UNITS = {"h": 3600, "m": 60, "s": 1}
_PART = re.compile(r"\s*(\d+)([a-z])\s*")


def parse_duration(text: str) -> int:
    """Return the number of seconds in a duration like "1h30m".

    - Units are h (hours), m (minutes) and s (seconds), case-insensitive.
    - Each part is a whole number followed by a unit, e.g. "2h", "45s".
    - Parts may be combined and separated by spaces: "1h 5m 10s".
    - Leading and trailing whitespace is ignored.
    - Raise ValueError for empty input, unknown units, a unit without a
      number, a number without a unit, decimals, or a unit used twice.
    """
    text = text.strip().lower()
    if not text:
        raise ValueError("empty duration")
    total, seen, pos = 0, set(), 0
    while pos < len(text):
        match = _PART.match(text, pos)
        if not match:
            raise ValueError(f"invalid duration {text!r}")
        number, unit = match.groups()
        if unit not in _UNITS:
            raise ValueError(f"unknown unit {unit!r}")
        if unit in seen:
            raise ValueError(f"unit {unit!r} used twice")
        seen.add(unit)
        total += int(number) * _UNITS[unit]
        pos = match.end()
    return total
