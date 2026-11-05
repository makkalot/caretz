package loopguard

import (
	"regexp"
	"strings"
)

const (
	maxFingerprintLines = 3
	maxFingerprintChars = 240
)

// normalizers strip the volatile parts of an error so the same failure
// repeated in a fresh temp dir, at another line, or at another time still
// compares equal. Order matters: specific shapes run before generic ones.
var normalizers = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`), ""},
	{regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`), "<id>"},
	{regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})?`), "<ts>"},
	{regexp.MustCompile(`\b\d{2}:\d{2}:\d{2}(\.\d+)?\b`), "<time>"},
	{regexp.MustCompile(`(?i)\b0x[0-9a-f]+\b`), "0xN"},
	{regexp.MustCompile(`(/private)?(/var/folders|/tmp|/var/tmp)(/[^\s:'"()]*)?`), "<tmp>"},
	{regexp.MustCompile(`(?:[A-Za-z]:)?(?:/[\w.\-@+~]+)+/([\w.\-@+]+)`), "<path>/$1"},
	{regexp.MustCompile(`(:\d+)+\b`), ":N"},
	{regexp.MustCompile(`(?i)\bline \d+`), "line N"},
	{regexp.MustCompile(`(?i)\bpid[ =:]?\d+`), "pid N"},
	{regexp.MustCompile(`\b\d+(\.\d+)?(ns|µs|us|ms|s|m|h)\b`), "<dur>"},
	{regexp.MustCompile(`\b\d{4,}\b`), "N"},
	{regexp.MustCompile(`[ \t]+`), " "},
}

// errorLine marks lines that usually carry the cause of a failure, so a long
// test log is fingerprinted by its errors rather than its first lines.
var errorLine = regexp.MustCompile(`(?i)\b(error|errors|fail|failed|failure|panic|fatal|exception|traceback|undefined|cannot|can't|no such|not found|denied|refused|expected|mismatch)\b`)

// Classify reports whether a tool result is a failure and, for failures,
// returns a fingerprint that is equal for near-identical errors. isErr is
// true when the tool itself returned an error; Bash also fails by exit code.
func Classify(toolName, content string, isErr bool) (fingerprint string, failed bool) {
	if !isErr && (toolName != "Bash" || !bashFailed(content)) {
		return "", false
	}
	return toolName + ": " + normalize(failureLines(content)), true
}

// bashFailed recognizes the Bash tool's failure shapes: a non-zero exit with
// stderr is reported as "sandbox error: <stderr>", and one without stderr
// ends with "error: <exit status>".
func bashFailed(content string) bool {
	if strings.HasPrefix(content, "sandbox error: ") || strings.Contains(content, "\nsandbox error: ") {
		return true
	}
	last := lastNonEmptyLine(content)
	return strings.HasPrefix(last, "error: ")
}

// failureLines picks the few lines that best identify a failure: lines that
// look like errors, else the last lines of output.
func failureLines(content string) []string {
	var lines, picked []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
		if errorLine.MatchString(line) && len(picked) < maxFingerprintLines {
			picked = append(picked, line)
		}
	}
	if len(picked) > 0 {
		return picked
	}
	if len(lines) > maxFingerprintLines {
		lines = lines[len(lines)-maxFingerprintLines:]
	}
	return lines
}

func normalize(lines []string) string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		for _, n := range normalizers {
			line = n.re.ReplaceAllString(line, n.repl)
		}
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return truncate(strings.Join(out, " | "), maxFingerprintChars)
}

func lastNonEmptyLine(content string) string {
	lines := strings.Split(strings.TrimRight(content, "\n "), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
