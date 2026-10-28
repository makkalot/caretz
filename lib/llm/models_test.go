package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListOpenAIModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Fatalf("request = %s %s, want GET /v1/models", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{
			{"id": "vendor/model"}, {"id": "gpt-5"}, {"id": "gpt-5"}, {"id": " "},
		}})
	}))
	defer server.Close()

	models, err := ListOpenAIModels(context.Background(), "secret", server.URL+"/v1", OpenAIProviderOptions{RequestTimeout: time.Second})
	if err != nil {
		t.Fatalf("ListOpenAIModels() error: %v", err)
	}
	if got, want := strings.Join(models, ","), "gpt-5,vendor/model"; got != want {
		t.Fatalf("models = %q, want %q", got, want)
	}
}

func TestListOpenAIModelsReportsProviderAndDecodeErrors(t *testing.T) {
	t.Run("provider error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
		}))
		defer server.Close()
		_, err := ListOpenAIModels(context.Background(), "bad", server.URL, OpenAIProviderOptions{RequestTimeout: time.Second})
		if err == nil || !strings.Contains(err.Error(), "bad key") {
			t.Fatalf("error = %v, want provider error", err)
		}
	})

	t.Run("malformed response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`not-json`))
		}))
		defer server.Close()
		_, err := ListOpenAIModels(context.Background(), "", server.URL, OpenAIProviderOptions{RequestTimeout: time.Second})
		if err == nil || !strings.Contains(err.Error(), "decode models response") {
			t.Fatalf("error = %v, want decode error", err)
		}
	})
}

func TestAPIErrorStatus(t *testing.T) {
	status, ok := APIErrorStatus(&apiError{StatusCode: http.StatusTooManyRequests})
	if !ok || status != http.StatusTooManyRequests {
		t.Fatalf("APIErrorStatus() = (%d, %v)", status, ok)
	}
	if _, ok := APIErrorStatus(context.DeadlineExceeded); ok {
		t.Fatal("APIErrorStatus() identified a non-API error")
	}
}
