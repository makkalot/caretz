package llm

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type ContextSizeError struct {
	PromptTokens int
	ContextSize  int
	Err          error
}

func (e ContextSizeError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("request prompt tokens %d exceed context size %d", e.PromptTokens, e.ContextSize)
}

func (e ContextSizeError) Unwrap() error {
	return e.Err
}

var (
	promptTokensPattern = regexp.MustCompile(`"n_prompt_tokens"\s*:\s*([0-9]+)|n_prompt_tokens[=:]\s*([0-9]+)`)
	contextSizePattern  = regexp.MustCompile(`"n_ctx"\s*:\s*([0-9]+)|n_ctx[=:]\s*([0-9]+)`)
)

func ParseContextSizeError(err error) (ContextSizeError, bool) {
	if err == nil {
		return ContextSizeError{}, false
	}
	var contextErr ContextSizeError
	if errors.As(err, &contextErr) {
		return contextErr, true
	}

	text := err.Error()
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "exceed_context_size_error") &&
		!strings.Contains(lower, "exceeds the available context size") &&
		!strings.Contains(lower, "context size") {
		return ContextSizeError{}, false
	}
	contextErr = ContextSizeError{
		PromptTokens: firstPatternInt(promptTokensPattern, text),
		ContextSize:  firstPatternInt(contextSizePattern, text),
		Err:          err,
	}
	return contextErr, true
}

func IsContextSizeError(err error) bool {
	_, ok := ParseContextSizeError(err)
	return ok
}

func firstPatternInt(pattern *regexp.Regexp, text string) int {
	matches := pattern.FindStringSubmatch(text)
	if len(matches) == 0 {
		return 0
	}
	for _, match := range matches[1:] {
		if match == "" {
			continue
		}
		value, err := strconv.Atoi(match)
		if err == nil {
			return value
		}
	}
	return 0
}
