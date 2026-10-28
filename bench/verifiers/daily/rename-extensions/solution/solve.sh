#!/usr/bin/env bash
set -euo pipefail
for f in photos/*.JPEG; do
  mv "$f" "${f%.JPEG}.jpg"
done
