#!/bin/bash
# Re-verify every code example in README.md, AGENTS.md, docs/, and the Hugo site.
# With pipefail, each `checker | tail` pipeline fails the run when the checker
# fails, not when tail does.
set -o pipefail
cd "$(dirname "$0")/.." || exit 1
# Default to the go on PATH; override with GO=/path/to/go for a pinned toolchain.
# GO is exported so the python checkers (gocompile.py) pick it up too.
GO=${GO:-go}
export GO
rc=0
echo "═══ extracting examples ═══"
python3 .doccheck/extract.py | head -2
echo
echo "═══ 1. Go examples compile ═══"
python3 .doccheck/gocompile.py | tail -2 || rc=1
echo
echo "═══ 2. gofmt ═══"
GO_ABS=$(command -v -- "$GO") || { echo "go binary not found: $GO"; exit 1; }
out=$("$(dirname "$GO_ABS")/gofmt" -l .doccheck/blocks/*.go 2>/dev/null)
[ -z "$out" ] && echo "clean" || { echo "NOT GOFMT: $out"; rc=1; }
echo
echo "═══ 3. shell syntax ═══"
for f in .doccheck/blocks/*.sh; do bash -n "$f" || rc=1; done && echo "clean"
echo
echo "═══ 4. CLI flags / make targets / tools / env vars ═══"
python3 .doccheck/cli_check.py | tail -3 || rc=1
echo
echo "═══ 5. config JSON examples ═══"
"$GO" build -o .doccheck/bin-cfgcheck ./.doccheck/cfgcheck 2>/dev/null
# Zed settings.json examples in docs/acp.md are third-party schema.
.doccheck/bin-cfgcheck .doccheck/blocks docs_acp__b02.json docs_acp__b03.json | tail -2 || rc=1
echo
echo "═══ 6. skill / subagent / AGENTS.md examples ═══"
"$GO" build -o .doccheck/bin-rescheck ./.doccheck/rescheck 2>/dev/null
.doccheck/bin-rescheck .doccheck/blocks | tail -2 || rc=1
echo
echo "═══ 7. hugo build ═══"
# Build first: the link check resolves Hugo page-relative links against
# site/public, so it needs a fresh rendered tree (CI has none).
(cd site && hugo --minify >/dev/null 2>&1) && echo "hugo ok" || { echo "hugo FAILED"; rc=1; }
echo
echo "═══ 8. links + anchors (md and rendered Hugo) ═══"
python3 .doccheck/links.py | tail -2 || rc=1
echo
echo "run_all rc=$rc"
exit $rc
