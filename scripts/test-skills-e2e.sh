#!/usr/bin/env bash
set -euo pipefail

# =============================================================================
# Skills E2E Validation Script
# =============================================================================
# This script validates the full skills feature end-to-end using real LLM calls.
# =============================================================================

# --- Configuration ---
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY="$PROJECT_DIR/bin/caretz"
ORIGINAL_HOME="${HOME}"
PASS_COUNT=0
FAIL_COUNT=0
TEST_DIRS=()

# Verify API key is available
if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    HOME_CONFIG="${HOME}/.config/caretz/config.json"
    if [[ ! -f "$HOME_CONFIG" ]]; then
        echo "Error: OPENAI_API_KEY not set and $HOME_CONFIG not found."
        exit 1
    fi
fi

# --- Helper Functions ---

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

setup_test_dir() {
    local tmpdir
    tmpdir=$(mktemp -d)
    mkdir -p "$tmpdir/home/.config/caretz"
    if [[ -f "$ORIGINAL_HOME/.config/caretz/config.json" ]]; then
        cp "$ORIGINAL_HOME/.config/caretz/config.json" "$tmpdir/home/.config/caretz/config.json"
    fi
    TEST_DIRS+=("$tmpdir")
    echo "$tmpdir"
}

create_test_skill() {
    local dir="$1"
    local name="$2"
    local description="$3"
    local body="$4"

    local skill_dir="$dir/.caretz/skills/$name"
    mkdir -p "$skill_dir"

    cat > "$skill_dir/SKILL.md" <<EOF
---
name: $name
description: $description
---

$body
EOF
}

cleanup() {
    if [[ ${#TEST_DIRS[@]} -gt 0 ]]; then
        for dir in "${TEST_DIRS[@]}"; do
            if [[ -d "$dir" ]]; then
                rm -rf "$dir"
            fi
        done
    fi
}

# Detect timeout command (macOS may need gtimeout from coreutils)
get_timeout_cmd() {
    if command -v timeout &> /dev/null; then
        echo "timeout"
    elif command -v gtimeout &> /dev/null; then
        echo "gtimeout"
    else
        echo ""
    fi
}

# --- Setup ---
echo "=== Skills E2E Test Suite ==="
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

# Set trap for cleanup
trap cleanup EXIT

TIMEOUT_CMD=$(get_timeout_cmd)
if [[ -z "$TIMEOUT_CMD" ]]; then
    echo "Warning: timeout/gtimeout not found. Tests may hang indefinitely."
    echo "         Install coreutils: brew install coreutils"
fi

# --- Test 1: Skill Discovery ---
echo "--- Test 1: Skill Discovery ---"
TEST1_DIR=$(setup_test_dir)

# Create test skill
create_test_skill "$TEST1_DIR" "test-greeter" \
    "Creates a greeting file when asked to greet someone" \
    "When the user asks you to greet them, use the Write tool to create a file called greeting.txt with content SKILL_E2E_PASSED."

# Run the agent
TEST1_STDOUT="$TEST1_DIR/stdout.log"
TEST1_STDERR="$TEST1_DIR/stderr.log"

if [[ -n "$TIMEOUT_CMD" ]]; then
    cd "$TEST1_DIR" && HOME="$TEST1_DIR/home" "$TIMEOUT_CMD" 120 "$BINARY" -p "say hello" -v > "$TEST1_STDOUT" 2> "$TEST1_STDERR" || true
else
    cd "$TEST1_DIR" && HOME="$TEST1_DIR/home" "$BINARY" -p "say hello" -v > "$TEST1_STDOUT" 2> "$TEST1_STDERR" || true
fi

# Verify stderr contains discovery messages
if grep -q "Discovered 1 skill" "$TEST1_STDERR"; then
    pass "Skill discovery logged 'Discovered 1 skill(s)'"
else
    fail "Skill discovery logged 'Discovered 1 skill(s)'" "Pattern not found in stderr"
fi

if grep -q "Skill: test-greeter" "$TEST1_STDERR"; then
    pass "Skill name 'test-greeter' found in logs"
else
    fail "Skill name 'test-greeter' found in logs" "Pattern not found in stderr"
fi

echo ""

# --- Test 2: Skill Execution ---
echo "--- Test 2: Skill Execution ---"
TEST2_DIR=$(setup_test_dir)

# Create test skill
create_test_skill "$TEST2_DIR" "test-greeter" \
    "Creates a greeting file when asked to greet someone" \
    "When the user asks you to greet them, do the following:

1. Use the Write tool to create a file called 'greeting.txt' in the current directory
2. The file content must be exactly: SKILL_E2E_PASSED
3. After writing the file, respond with 'Greeting complete!'"

TEST2_STDOUT="$TEST2_DIR/stdout.log"
TEST2_STDERR="$TEST2_DIR/stderr.log"

if [[ -n "$TIMEOUT_CMD" ]]; then
    cd "$TEST2_DIR" && HOME="$TEST2_DIR/home" "$TIMEOUT_CMD" 120 "$BINARY" -p "Use the test-greeter skill to greet me" -v > "$TEST2_STDOUT" 2> "$TEST2_STDERR" || true
else
    cd "$TEST2_DIR" && HOME="$TEST2_DIR/home" "$BINARY" -p "Use the test-greeter skill to greet me" -v > "$TEST2_STDOUT" 2> "$TEST2_STDERR" || true
fi

# Verify the greeting file was created
if [[ -f "$TEST2_DIR/greeting.txt" ]]; then
    pass "greeting.txt was created"
else
    fail "greeting.txt was created" "File not found in $TEST2_DIR"
fi

# Verify the file content
if [[ -f "$TEST2_DIR/greeting.txt" ]] && grep -q "SKILL_E2E_PASSED" "$TEST2_DIR/greeting.txt"; then
    pass "greeting.txt contains 'SKILL_E2E_PASSED'"
else
    fail "greeting.txt contains 'SKILL_E2E_PASSED'" "Content not found in file"
fi

echo ""

# --- Test 3: No Skills Directory ---
echo "--- Test 3: No Skills Directory ---"
TEST3_DIR=$(setup_test_dir)

# Just an empty temp directory, no .caretz/skills/
TEST3_STDOUT="$TEST3_DIR/stdout.log"
TEST3_STDERR="$TEST3_DIR/stderr.log"

if [[ -n "$TIMEOUT_CMD" ]]; then
    cd "$TEST3_DIR" && HOME="$TEST3_DIR/home" "$TIMEOUT_CMD" 120 "$BINARY" -p "say hello" -v > "$TEST3_STDOUT" 2> "$TEST3_STDERR"
    TEST3_EXIT=$?
else
    cd "$TEST3_DIR" && HOME="$TEST3_DIR/home" "$BINARY" -p "say hello" -v > "$TEST3_STDOUT" 2> "$TEST3_STDERR"
    TEST3_EXIT=$?
fi

if [[ $TEST3_EXIT -eq 0 ]]; then
    pass "Agent ran successfully without skills directory"
else
    fail "Agent ran successfully without skills directory" "Exit code was $TEST3_EXIT"
fi

# Verify no discovery logs
if ! grep -q "Discovered" "$TEST3_STDERR"; then
    pass "No discovery logs when no skills exist"
else
    fail "No discovery logs when no skills exist" "Found 'Discovered' in stderr"
fi

echo ""

# --- Test 4: Invalid Skill Skipped ---
echo "--- Test 4: Invalid Skill Skipped ---"
TEST4_DIR=$(setup_test_dir)

# Create an invalid skill (missing description)
mkdir -p "$TEST4_DIR/.caretz/skills/bad-skill"
cat > "$TEST4_DIR/.caretz/skills/bad-skill/SKILL.md" <<EOF
---
name: bad-skill
---

This skill is missing the description field.
EOF

# Create a valid skill alongside it
create_test_skill "$TEST4_DIR" "good-skill" \
    "A valid skill for testing" \
    "This is a valid skill."

TEST4_STDOUT="$TEST4_DIR/stdout.log"
TEST4_STDERR="$TEST4_DIR/stderr.log"

if [[ -n "$TIMEOUT_CMD" ]]; then
    cd "$TEST4_DIR" && HOME="$TEST4_DIR/home" "$TIMEOUT_CMD" 120 "$BINARY" -p "say hello" -v > "$TEST4_STDOUT" 2> "$TEST4_STDERR" || true
else
    cd "$TEST4_DIR" && HOME="$TEST4_DIR/home" "$BINARY" -p "say hello" -v > "$TEST4_STDOUT" 2> "$TEST4_STDERR" || true
fi

# Verify only 1 skill was discovered (the valid one)
if grep -q "Discovered 1 skill" "$TEST4_STDERR"; then
    pass "Only valid skill discovered (1, not 2)"
else
    # Check if it shows 2 skills (which would be a failure)
    if grep -q "Discovered 2 skill" "$TEST4_STDERR"; then
        fail "Only valid skill discovered (1, not 2)" "Found 2 skills instead of 1 (invalid skill was not skipped)"
    else
        fail "Only valid skill discovered (1, not 2)" "Could not determine skill count from stderr"
    fi
fi

echo ""

# --- Summary ---
echo "=== Results: $PASS_COUNT passed, $FAIL_COUNT failed ==="

if [[ $FAIL_COUNT -gt 0 ]]; then
    exit 1
else
    exit 0
fi
