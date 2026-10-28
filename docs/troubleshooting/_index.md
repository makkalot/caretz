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

Logs go to stderr, while the final one-shot answer goes to stdout. This makes
it possible to redirect the response without mixing in diagnostics.

Sandbox errors on a specific platform are covered in
[Linux](../sandboxing/linux.md) and [macOS](../sandboxing/macos.md).
