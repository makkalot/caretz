---
title: "Themes"
weight: 30
description: "Pick a color theme, or keep your terminal's own colors."
---

# Color Themes

Caretz follows your terminal's own colors by default. If you'd rather use a
familiar editor palette, pick a theme for one run:

```sh
caretz --theme catppuccin-mocha
```

Inside a session, `/theme` shows every theme with a sample of its colors, and
`/theme dracula` switches right away. The switch lasts until you exit. To keep
a theme, set it in `~/.config/caretz/config.json`:

```json
{
  "profiles": {
    "general": {
      "output": {"theme": "tokyo-night"}
    }
  }
}
```

`CARETZ_THEME=github-dark` does the same for one shell. The `--theme` flag
overrides `CARETZ_THEME`, which overrides the config file.

| Theme | Looks like |
| --- | --- |
| `ansi` | Your terminal's own 16 colors (default) |
| `catppuccin-mocha` | Catppuccin Mocha |
| `dracula` | Dracula |
| `github-dark` | GitHub Dark |
| `tokyo-night` | Tokyo Night |

All themes except `ansi` are dark palettes meant for a dark terminal background.

## Over SSH and in Limited Terminals

Caretz checks `COLORTERM` and `TERM` to find out how many colors your terminal
supports:

- `COLORTERM=truecolor` or `24bit`: themes use their exact colors.
- A `TERM` ending in `256color`: themes use the nearest of 256 colors.
- Anything else: every theme falls back to `ansi`, which only uses your
  terminal's palette.

SSH sessions often drop `COLORTERM`. If colors look off on a remote machine,
use `--theme ansi`, or forward `COLORTERM` when your terminal supports full
color. `NO_COLOR=1` or piping output turns colors off entirely.
