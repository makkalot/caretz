package story

import (
	"fmt"
	"regexp"
	"strings"
)

// Matcher checks a string and describes the check in plain language, so any
// Then subject (stdout, a file, a message sent to the model) can be combined
// with any matcher and still read as one sentence in the .feature file.
type Matcher struct {
	desc    string // "contains \"x\""
	negDesc string // "does not contain \"x\""
	match   func(got string) bool
}

// Contains matches a string that contains want.
func Contains(want string) Matcher {
	return Matcher{
		desc:    fmt.Sprintf("contains %q", want),
		negDesc: fmt.Sprintf("does not contain %q", want),
		match:   func(got string) bool { return strings.Contains(got, want) },
	}
}

// Equals matches a string that is exactly want.
func Equals(want string) Matcher {
	return Matcher{
		desc:    fmt.Sprintf("is %q", want),
		negDesc: fmt.Sprintf("is not %q", want),
		match:   func(got string) bool { return got == want },
	}
}

// Matches matches a string against a regular expression.
func Matches(pattern string) Matcher {
	re := regexp.MustCompile(pattern)
	return Matcher{
		desc:    fmt.Sprintf("matches the pattern %q", pattern),
		negDesc: fmt.Sprintf("does not match the pattern %q", pattern),
		match:   re.MatchString,
	}
}

// Not inverts m.
func Not(m Matcher) Matcher {
	return Matcher{
		desc:    m.negDesc,
		negDesc: m.desc,
		match:   func(got string) bool { return !m.match(got) },
	}
}

// String returns the matcher's plain-language description.
func (m Matcher) String() string { return m.desc }
