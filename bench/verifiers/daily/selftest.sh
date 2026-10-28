#!/usr/bin/env bash
# Check every daily-suite verifier without a model: each must FAIL on the
# untouched seed workspace and PASS on its reference solution.
#
# A reference solution lives in verifiers/daily/<id>/solution/. Its files are
# copied over the workspace, then solution/solve.sh (if present) runs inside it.
#
# Usage: bench/verifiers/daily/selftest.sh [TASK_ID]
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
BENCH_ROOT="$(cd "$here/../.." && pwd)"
export BENCH_ROOT
suite="$BENCH_ROOT/suites/daily.yaml"
only="${1:-}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

run_verifier() {
  local id="$1" ws="$2" log="$3"
  (cd "$ws" && python3 -I "$here/$id/verify.py") >"$log" 2>&1
}

status=0
count=0
for id in $(sed -n 's/^  - id: *//p' "$suite"); do
  if [ -n "$only" ] && [ "$id" != "$only" ]; then
    continue
  fi
  count=$((count + 1))
  seed="$BENCH_ROOT/workspaces/daily/$id"
  solution="$here/$id/solution"
  for need in "$seed" "$here/$id/verify.py" "$solution"; do
    if [ ! -e "$need" ]; then
      echo "FAIL $id: missing $need"
      status=1
      continue 2
    fi
  done
  if ! grep -q "verifiers/daily/$id/verify.py" "$suite"; then
    echo "FAIL $id: suite evaluator_command does not run verifiers/daily/$id/verify.py"
    status=1
    continue
  fi

  cp -R "$seed" "$tmp/$id-seed"
  if run_verifier "$id" "$tmp/$id-seed" "$tmp/$id-seed.log"; then
    echo "FAIL $id: verifier passes on the untouched workspace"
    status=1
    continue
  fi

  cp -R "$seed" "$tmp/$id-solved"
  (cd "$solution" && tar cf - --exclude ./solve.sh .) | (cd "$tmp/$id-solved" && tar xf -)
  if [ -f "$solution/solve.sh" ]; then
    (cd "$tmp/$id-solved" && bash "$solution/solve.sh") >"$tmp/$id-solve.log" 2>&1 || {
      echo "FAIL $id: solution/solve.sh failed"
      sed 's/^/    /' "$tmp/$id-solve.log"
      status=1
      continue
    }
  fi
  if run_verifier "$id" "$tmp/$id-solved" "$tmp/$id-solved.log"; then
    echo "ok   $id ($(sed -n 's/^FAIL: //p' "$tmp/$id-seed.log" | head -n 1))"
  else
    echo "FAIL $id: verifier rejects the reference solution"
    sed 's/^/    /' "$tmp/$id-solved.log"
    status=1
  fi
done

if [ "$count" -eq 0 ]; then
  echo "no tasks matched ${only:-the suite}"
  exit 1
fi
exit "$status"
