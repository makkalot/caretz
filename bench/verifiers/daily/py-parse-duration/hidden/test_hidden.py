import unittest

from duration import parse_duration


class ParseDurationTest(unittest.TestCase):
    def test_valid(self):
        cases = {
            "1h30m": 5400,
            "45s": 45,
            "2h": 7200,
            "1h 5m 10s": 3910,
            "10M": 600,
            "0s": 0,
            "  3m  ": 180,
            "90m": 5400,
            "1H2M3S": 3723,
            "5s 1h": 3605,
        }
        for text, want in cases.items():
            with self.subTest(text=text):
                self.assertEqual(parse_duration(text), want)

    def test_invalid(self):
        for text in ["", "   ", "abc", "5x", "h", "10", "1h1h", "1.5h", "-5m", "1h 2m 3m"]:
            with self.subTest(text=text):
                with self.assertRaises(ValueError):
                    parse_duration(text)


if __name__ == "__main__":
    unittest.main()
