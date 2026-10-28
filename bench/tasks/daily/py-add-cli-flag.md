Please add two options to `wordlist.py`:

- `--reverse`: print the words in reverse alphabetical order (Z to A).
- `--limit N`: print at most N words, applied after sorting. N must be a positive whole number. Reject anything else, such as `0`, `-1`, or `abc`, with a normal argparse usage error.

The options can be combined and can appear before or after the file path. With no options, the output must stay exactly as it is now. You can try it on `sample.txt`.
