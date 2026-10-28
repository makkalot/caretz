package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/makkalot/caretz/lib/llm"
)

// StructuredValidator lets structured response targets enforce semantic rules
// that cannot be expressed by validate:"required" tags, such as fields that
// are required only for a particular discriminator value.
type StructuredValidator interface {
	ValidateStructured() error
}

// StructuredCandidateValidator lets a preconfigured target validate a decoded
// candidate against caller-owned context that is not part of the JSON output.
type StructuredCandidateValidator interface {
	ValidateStructuredCandidate(candidate any) error
}

// RunStructured runs the agent and decodes its final JSON object into target.
// target must be a non-nil pointer. Fields tagged validate:"required" must be
// nonzero. An invalid response gets up to four correction attempts in the same session.
// target is changed only after a response has decoded and passed validation.
func (a *AgentSession) RunStructured(ctx context.Context, prompt string, target any) (*llm.Response, error) {
	value := reflect.ValueOf(target)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() {
		return nil, fmt.Errorf("structured output target must be a non-nil pointer")
	}
	request := prompt + "\nReply with exactly one JSON object matching the requested structure."
	if fields := structuredFields(value.Elem().Type()); fields != "" {
		request += "\nExpected JSON fields (type descriptions, not literal values): " + fields
	}
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		response, err := a.Call(ctx, request)
		if err != nil {
			return nil, err
		}
		candidate := reflect.New(value.Elem().Type())
		if err := decodeStructuredResponse(response, candidate.Interface()); err == nil {
			if err = validateStructuredValue(candidate.Elem(), ""); err == nil {
				if validator, ok := candidate.Interface().(StructuredValidator); ok {
					err = validator.ValidateStructured()
				}
				if err == nil {
					if validator, ok := target.(StructuredCandidateValidator); ok {
						err = validator.ValidateStructuredCandidate(candidate.Interface())
					}
				}
				if err == nil {
					value.Elem().Set(candidate.Elem())
					return response, nil
				}
			}
			lastErr = err
		} else {
			lastErr = err
		}
		request = fmt.Sprintf("Your previous response was invalid JSON or failed validation (%v). Reply with one corrected JSON object matching the requested structure. No prose, code fence, or tool calls.", lastErr)
	}
	return nil, fmt.Errorf("structured output invalid after %d attempts: %w", maxAttempts, lastErr)
}

func structuredFields(typ reflect.Type) string {
	if typ.Kind() != reflect.Struct {
		return ""
	}
	fields := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() || field.Tag.Get("json") == "-" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" {
			name = field.Name
		}
		kind := field.Type
		for kind.Kind() == reflect.Pointer {
			kind = kind.Elem()
		}
		description := kind.Kind().String()
		switch {
		case kind.PkgPath() == "time" && kind.Name() == "Time":
			description = "RFC3339 timestamp"
		case kind.Kind() >= reflect.Int && kind.Kind() <= reflect.Float64:
			description = "number"
		case kind.Kind() == reflect.Slice || kind.Kind() == reflect.Array:
			description = "array"
		case kind.Kind() == reflect.Map || kind.Kind() == reflect.Struct:
			description = "object"
		}
		if hasStructuredRule(field.Tag.Get("validate"), "required") {
			description += ", required"
		}
		fields = append(fields, name+" ("+description+")")
	}
	return strings.Join(fields, "; ")
}

func decodeStructuredResponse(response *llm.Response, target any) error {
	if response == nil {
		return fmt.Errorf("empty response")
	}
	content := response.Content
	if content == "" {
		content = response.Message.Content
	}
	content = strings.TrimSpace(content)
	if start := strings.IndexByte(content, '{'); start >= 0 {
		content = content[start:]
	}
	if end := strings.LastIndexByte(content, '}'); end >= 0 {
		content = content[:end+1]
	}
	if !strings.HasPrefix(content, "{") || !strings.HasSuffix(content, "}") {
		return fmt.Errorf("expected a JSON object")
	}
	if err := json.Unmarshal([]byte(content), target); err != nil {
		return err
	}
	return nil
}

func validateStructuredValue(value reflect.Value, path string) error {
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return nil
	}
	typ := value.Type()
	for i := 0; i < value.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() || field.Tag.Get("json") == "-" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" {
			name = field.Name
		}
		if path != "" {
			name = path + "." + name
		}
		child := value.Field(i)
		if hasStructuredRule(field.Tag.Get("validate"), "required") && structuredEmpty(child) {
			return fmt.Errorf("%s is required", name)
		}
		if err := validateStructuredValue(child, name); err != nil {
			return err
		}
	}
	return nil
}

func hasStructuredRule(tag, desired string) bool {
	for _, rule := range strings.Split(tag, ",") {
		if strings.TrimSpace(rule) == desired {
			return true
		}
	}
	return false
}

func structuredEmpty(value reflect.Value) bool {
	if !value.IsValid() {
		return true
	}
	switch value.Kind() {
	case reflect.String:
		return strings.TrimSpace(value.String()) == ""
	case reflect.Slice, reflect.Map, reflect.Array:
		return value.Len() == 0
	default:
		return value.IsZero()
	}
}
