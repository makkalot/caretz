---
title: "Troubleshooting"
description: "Start here: version and logs."
---

# Troubleshooting

Start by checking the installed build and enabling logs:

```sh
caretz --version
caretz -v -p "hello"       # runtime decisions and tool activity
caretz -vv -p "hello"      # debug detail
```

Without `-v`, `-p` prints only the final answer, as raw text on stdout, so it
pipes cleanly into other tools; errors still go to stderr. `-v` and `-vv` add
the model, request, and tool activity on stderr.

Sandbox errors on a specific platform are covered in
[Linux](../sandboxing/linux.md) and [macOS](../sandboxing/macos.md).
