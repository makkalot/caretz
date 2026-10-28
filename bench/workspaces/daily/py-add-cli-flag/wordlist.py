"""Print the unique words in a text file, one per line, in alphabetical order."""

import argparse
import re


def unique_words(text):
    return sorted({word.lower() for word in re.findall(r"[A-Za-z']+", text)})


def main(argv=None):
    parser = argparse.ArgumentParser(description="Print the unique words in a text file.")
    parser.add_argument("path", help="text file to read")
    args = parser.parse_args(argv)

    with open(args.path, encoding="utf-8") as f:
        words = unique_words(f.read())
    for word in words:
        print(word)


if __name__ == "__main__":
    main()
