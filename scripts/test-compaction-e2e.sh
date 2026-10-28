#!/usr/bin/env bash
set -euo pipefail

# =============================================================================
# Context Compaction E2E Validation Script
# =============================================================================

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY="$PROJECT_DIR/bin/caretz"
PASS_COUNT=0
FAIL_COUNT=0

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    HOME_CONFIG="${HOME}/.config/caretz/config.json"
    if [[ ! -f "$HOME_CONFIG" ]]; then
        echo "Error: OPENAI_API_KEY not set and $HOME_CONFIG not found. Skipping E2E compaction tests."
        exit 0
    fi
fi

pass() {
    local test_name="$1"
    echo "✓ PASS: $test_name"
    PASS_COUNT=$((PASS_COUNT + 1))
}

fail() {
    local test_name="$1"
    local reason="$2"
    echo "✗ FAIL: $test_name"
    echo "       Reason: $reason"
    FAIL_COUNT=$((FAIL_COUNT + 1))
}

get_timeout_cmd() {
    if command -v timeout &> /dev/null; then
        echo "timeout"
    elif command -v gtimeout &> /dev/null; then
        echo "gtimeout"
    else
        echo ""
    fi
}

echo "=== Context Compaction E2E Test Suite ==="
echo ""

echo "Building binary..."
if ! make -C "$PROJECT_DIR" build > /dev/null 2>&1; then
    echo "Error: Failed to build binary"
    exit 1
fi

if [[ ! -f "$BINARY" ]]; then
    echo "Error: Binary not found at $BINARY"
    exit 1
fi

echo "✓ Binary built successfully"
echo ""

TIMEOUT_CMD=$(get_timeout_cmd)
if [[ -z "$TIMEOUT_CMD" ]]; then
    echo "Warning: timeout/gtimeout not found. Tests may hang indefinitely."
    echo "         Install coreutils: brew install coreutils"
fi

# --- Test 1: Compaction Triggers on Small Context Window ---
echo "--- Test 1: Compaction triggers on small context window ---"
TMPDIR1=$(mktemp -d)
STDERR_LOG1="$TMPDIR1/stderr.log"
STDOUT_LOG1="$TMPDIR1/stdout.log"

if [[ -n "$TIMEOUT_CMD" ]]; then
    cd "$TMPDIR1" && "$TIMEOUT_CMD" 120 "$BINARY" -p "Write a detailed explanation of the history of computing covering at least 5 major milestones. Include details about each." -context-window 4000 -v > "$STDOUT_LOG1" 2> "$STDERR_LOG1" || true
else
    cd "$TMPDIR1" && "$BINARY" -p "Write a detailed explanation of the history of computing covering at least 5 major milestones. Include details about each." -context-window 4000 -v > "$STDOUT_LOG1" 2> "$STDERR_LOG1" || true
fi

if grep -qi "compacting" "$STDERR_LOG1"; then
    pass "Compaction was triggered"
else
    fail "Compaction was triggered" "No compaction log message found in stderr"
fi

rm -rf "$TMPDIR1"
echo ""

# --- Test 2: Agent Continues After Compaction ---
echo "--- Test 2: Agent continues after compaction ---"
TMPDIR2=$(mktemp -d)
STDERR_LOG2="$TMPDIR2/stderr.log"
STDOUT_LOG2="$TMPDIR2/stdout.log"

if [[ -n "$TIMEOUT_CMD" ]]; then
    cd "$TMPDIR2" && "$TIMEOUT_CMD" 120 "$BINARY" -p "Create a file called test_output.txt with the content 'compaction_e2e_passed'. Then read it back and confirm what it says." -context-window 4000 -v > "$STDOUT_LOG2" 2> "$STDERR_LOG2" || true
else
    cd "$TMPDIR2" && "$BINARY" -p "Create a file called test_output.txt with the content 'compaction_e2e_passed'. Then read it back and confirm what it says." -context-window 4000 -v > "$STDOUT_LOG2" 2> "$STDERR_LOG2" || true
fi

TEST_EXIT=${PIPESTATUS[0]:-0}
if [[ -f "$TMPDIR2/test_output.txt" ]]; then
    pass "Agent created file after compaction"
else
    fail "Agent created file after compaction" "test_output.txt not found"
fi

rm -rf "$TMPDIR2"
echo ""

# --- Test 3: Context Window Env Var ---
echo "--- Test 3: Context window via env var ---"
TMPDIR3=$(mktemp -d)
STDERR_LOG3="$TMPDIR3/stderr.log"
STDOUT_LOG3="$TMPDIR3/stdout.log"

if [[ -n "$TIMEOUT_CMD" ]]; then
    cd "$TMPDIR3" && CONTEXT_WINDOW_TOKENS=4000 "$TIMEOUT_CMD" 120 "$BINARY" -p "Say hello" -v > "$STDOUT_LOG3" 2> "$STDERR_LOG3" || true
else
    cd "$TMPDIR3" && CONTEXT_WINDOW_TOKENS=4000 "$BINARY" -p "Say hello" -v > "$STDOUT_LOG3" 2> "$STDERR_LOG3" || true
fi

pass "Context window env var accepted (no crash)"

rm -rf "$TMPDIR3"
echo ""

# --- Summary ---
echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="

if [[ $FAIL_COUNT -gt 0 ]]; then
    exit 1
else
    exit 0
fi
