#!/usr/bin/env bash
# Install the reference backup.sh into the workspace.
set -euo pipefail
cp "$(dirname "$0")/backup.sh.txt" backup.sh
chmod +x backup.sh
