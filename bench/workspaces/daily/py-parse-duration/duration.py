"""Parse human-friendly durations for the reminder CLI."""


def parse_duration(text: str) -> int:
    """Return the number of seconds in a duration like "1h30m".

    - Units are h (hours), m (minutes) and s (seconds), case-insensitive.
    - Each part is a whole number followed by a unit, e.g. "2h", "45s".
    - Parts may be combined and separated by spaces: "1h 5m 10s".
    - Leading and trailing whitespace is ignored.
    - Raise ValueError for empty input, unknown units, a unit without a
      number, a number without a unit, decimals, or a unit used twice.
    """
    raise NotImplementedError
