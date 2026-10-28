"""Print the unique words in a text file, one per line, in alphabetical order."""

import argparse
import re


def unique_words(text):
    return sorted({word.lower() for word in re.findall(r"[A-Za-z']+", text)})


def positive_int(value):
    try:
        number = int(value)
    except ValueError:
        raise argparse.ArgumentTypeError(f"{value!r} is not a whole number")
    if number <= 0:
        raise argparse.ArgumentTypeError(f"{value} must be positive")
    return number


def main(argv=None):
    parser = argparse.ArgumentParser(description="Print the unique words in a text file.")
    parser.add_argument("path", help="text file to read")
    parser.add_argument("--reverse", action="store_true", help="sort Z to A")
    parser.add_argument("--limit", type=positive_int, help="print at most N words")
    args = parser.parse_args(argv)

    with open(args.path, encoding="utf-8") as f:
        words = unique_words(f.read())
    if args.reverse:
        words.reverse()
    if args.limit is not None:
        words = words[: args.limit]
    for word in words:
        print(word)


if __name__ == "__main__":
    main()
