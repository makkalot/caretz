#!/usr/bin/env bash
set -euo pipefail
cd Downloads
mkdir -p documents images archives
for f in *; do
  [ -f "$f" ] || continue
  lower="$(printf '%s' "$f" | tr '[:upper:]' '[:lower:]')"
  case "$lower" in
    *.pdf | *.docx | *.txt | *.md | *.xlsx | *.csv) mv -- "$f" documents/ ;;
    *.jpg | *.jpeg | *.png | *.gif | *.heic) mv -- "$f" images/ ;;
    *.zip | *.tar.gz | *.7z) mv -- "$f" archives/ ;;
  esac
done
