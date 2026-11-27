package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
)

func TestIsUnreachable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedURL := "http://" + listener.Addr().String()
	_ = listener.Close()
	_, refused := http.Get(closedURL)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer server.Close()
	_, serverErr := NewOpenAIProviderWithOptions("key", server.URL, "model", OpenAIProviderOptions{MaxAttempts: 1}).Chat("", []Message{{Role: RoleUser, Content: "hi"}}, nil, nil)

	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"connection refused":    {refused, true},
		"wrapped after retries": {fmt.Errorf("LLM request failed after 3 attempts: %w", refused), true},
		"dns failure":           {&net.DNSError{Err: "no such host", Name: "nowhere.invalid", IsNotFound: true}, true},
		"host unreachable":      {fmt.Errorf("post: %w", syscall.EHOSTUNREACH), true},
		"http 500":              {serverErr, false},
		"deadline":              {fmt.Errorf("judge: %w", context.DeadlineExceeded), false},
		"cancelled":             {context.Canceled, false},
		"other":                 {errors.New("malformed verdict"), false},
		"nil":                   {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.err == nil && tc.want {
				t.Fatal("test setup produced no error")
			}
			if got := IsUnreachable(tc.err); got != tc.want {
				t.Fatalf("IsUnreachable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
