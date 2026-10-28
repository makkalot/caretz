package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/config"
)

// modelsTestConfig writes a home config with one live provider, one that
// answers 401, and two unreachable ones, with the active model on "gone".
func modelsTestConfig(t *testing.T) string {
	t.Helper()
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"live-model"}]}`)
	}))
	t.Cleanup(live.Close)
	locked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"missing key"}}`, http.StatusUnauthorized)
	}))
	t.Cleanup(locked.Close)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + listener.Addr().String() + "/v1"
	_ = listener.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	data := `{"keep":{"me":true},"activeModel":"gone/old",
"provider":{
 "live":{"options":{"baseURL":"` + live.URL + `"}},
 "locked":{"options":{"baseURL":"` + locked.URL + `"}},
 "gone":{"options":{"baseURL":"` + closed + `"},"models":{"old":{}}},
 "stale":{"options":{"baseURL":"` + closed + `"},"models":{"x":{}}}
}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func providerURL(t *testing.T, path, key string) string {
	t.Helper()
	cfg, err := config.Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	return *cfg.Provider[key].Options.BaseURL
}

func fixedNow() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) }

func TestModelsCommandListReadsCacheWithoutNetwork(t *testing.T) {
	path := modelsTestConfig(t)
	// A probe would add live-model; the cache says cached-model.
	err := config.UpdateModelCache(config.ModelCachePath(path), func(cache *config.ModelCache) {
		cache.SetCachedModels("live", providerURL(t, path, "live"), []string{"cached-model"}, fixedNow().Add(-3*time.Hour))
	})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	cmd := modelsCommand{homePath: path, out: &out, errOut: io.Discard, timeout: time.Second, now: fixedNow}
	if code := cmd.list(); code != 0 {
		t.Fatalf("code = %d", code)
	}
	got := out.String()
	for _, want := range []string{
		"live  fetched 3h ago", "    live/cached-model",
		"gone  ○ never fetched", "  * gone/old",
		"caretz models --refresh",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "live-model") {
		t.Fatalf("list contacted a provider:\n%s", got)
	}
}

func TestModelsCommandRefreshCachesAndKeepsFailedEntries(t *testing.T) {
	path := modelsTestConfig(t)
	cachePath := config.ModelCachePath(path)
	err := config.UpdateModelCache(cachePath, func(cache *config.ModelCache) {
		cache.SetCachedModels("gone", providerURL(t, path, "gone"), []string{"remembered"}, fixedNow().Add(-time.Hour))
	})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	cmd := modelsCommand{homePath: path, out: &out, errOut: io.Discard, timeout: time.Second, now: fixedNow}
	if code := cmd.refresh(context.Background()); code != 0 {
		t.Fatalf("code = %d\n%s", code, out.String())
	}
	got := out.String()
	for _, want := range []string{
		"live  ● online, 1 models", "    live/live-model",
		"gone  ⚠ unreachable: connection refused", "    gone/remembered",
		"locked  ⚠ key rejected (HTTP 401); run caretz auth set locked",
		"3 of 4 providers failed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	cache, err := config.LoadModelCache(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if live := cache.Provider["live"]; len(live.Models) != 1 || live.Models[0] != "live-model" || !live.FetchedAt.Equal(fixedNow()) {
		t.Fatalf("live cache = %+v", live)
	}
	if gone := cache.Provider["gone"]; len(gone.Models) != 1 || gone.Models[0] != "remembered" {
		t.Fatalf("failed probe replaced cached entry: %+v", gone)
	}
}

func TestModelsCommandSeesConfigRootProviders(t *testing.T) {
	path := modelsTestConfig(t)
	corp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":[{"id":"corp-model"}]}`)
	}))
	t.Cleanup(corp.Close)
	rootConfig := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(rootConfig, []byte(`{"provider":{"corp":{"options":{"baseURL":"`+corp.URL+`"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	cmd := modelsCommand{homePath: path, projectPath: rootConfig, projectTrusted: true, out: &out, errOut: io.Discard, timeout: time.Second, now: fixedNow}
	if code := cmd.refresh(context.Background()); code != 0 {
		t.Fatalf("code = %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "corp  ● online, 1 models") || !strings.Contains(out.String(), "    corp/corp-model") {
		t.Fatalf("config root provider not refreshed:\n%s", out.String())
	}
	cache, err := config.LoadModelCache(config.ModelCachePath(path))
	if err != nil {
		t.Fatal(err)
	}
	if corp := cache.Provider["corp"]; len(corp.Models) != 1 || corp.Models[0] != "corp-model" {
		t.Fatalf("corp cache = %+v", corp)
	}

	// A workspace .caretz/config.json may not define providers.
	cmd.projectTrusted = false
	if code := cmd.list(); code != 1 {
		t.Fatalf("untrusted project providers loaded: code = %d", code)
	}
}

func TestModelsCommandPruneDeletesChosenProviders(t *testing.T) {
	path := modelsTestConfig(t)
	var out strings.Builder
	// Providers are asked in key order: gone, stale.
	cmd := modelsCommand{homePath: path, in: strings.NewReader("y\nn\n"), out: &out, errOut: io.Discard, timeout: time.Second}
	if code := cmd.prune(context.Background()); code != 0 {
		t.Fatalf("code = %d\n%s", code, out.String())
	}
	if strings.Contains(out.String(), "Delete locked?") {
		t.Fatalf("provider that answered 401 offered for pruning:\n%s", out.String())
	}
	for _, want := range []string{"2 unreachable providers", "active model gone/old uses this provider", "Removed gone and their API keys from", "picks an available model"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	cfg, err := config.Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Provider["gone"]; ok {
		t.Fatal("gone was not removed")
	}
	for _, key := range []string{"live", "locked", "stale"} {
		if _, ok := cfg.Provider[key]; !ok {
			t.Fatalf("%s was removed", key)
		}
	}
	if cfg.ActiveModel != "" {
		t.Fatalf("ActiveModel = %q, want cleared", cfg.ActiveModel)
	}
}

func TestModelsCommandPruneCancelLeavesConfig(t *testing.T) {
	path := modelsTestConfig(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var errOut strings.Builder
	cmd := modelsCommand{homePath: path, in: strings.NewReader("y\n"), out: io.Discard, errOut: &errOut, timeout: time.Second}
	if code := cmd.prune(context.Background()); code != 1 {
		t.Fatalf("code = %d, want 1 on EOF", code)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) || !strings.Contains(errOut.String(), "not changed") {
		t.Fatalf("config changed on cancel or missing notice: %s", errOut.String())
	}
}

func TestModelsCommandRemoveDeletesNamedProviders(t *testing.T) {
	path := modelsTestConfig(t)
	var out strings.Builder
	cmd := modelsCommand{homePath: path, in: strings.NewReader("y\n"), out: &out, errOut: io.Discard}
	// Removing does not probe, so a reachable provider goes as readily.
	if code := cmd.remove([]string{"live", "gone", "live"}); code != 0 {
		t.Fatalf("code = %d\n%s", code, out.String())
	}
	// The preview, shown before the question, names the provider and the
	// active model it takes with it.
	preview, _, _ := strings.Cut(out.String(), "Remove live, gone? (y/N)")
	for _, want := range []string{"  gone  http://", "activeModel; caretz picks an available model"} {
		if !strings.Contains(preview, want) {
			t.Fatalf("preview missing %q:\n%s", want, out.String())
		}
	}
	for _, want := range []string{"Remove live, gone? (y/N)", "Removed live, gone from"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	cfg, err := config.Load(path, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"live", "gone"} {
		if _, ok := cfg.Provider[key]; ok {
			t.Fatalf("%s was not removed", key)
		}
	}
	if _, ok := cfg.Provider["stale"]; !ok {
		t.Fatal("stale was removed")
	}
}

func TestModelsCommandRemoveDeclinedLeavesConfig(t *testing.T) {
	path := modelsTestConfig(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, answer := range []string{"n\n", "\n", ""} {
		var errOut strings.Builder
		cmd := modelsCommand{homePath: path, in: strings.NewReader(answer), out: io.Discard, errOut: &errOut}
		if code := cmd.remove([]string{"gone"}); code != 1 || !strings.Contains(errOut.String(), "not changed") {
			t.Fatalf("answer %q: code = %d, stderr %q", answer, code, errOut.String())
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("config changed by a declined remove:\n%s", after)
	}
}

func TestModelsCommandRemoveRefusesUnknownAndConfigRootProviders(t *testing.T) {
	path := modelsTestConfig(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rootConfig := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(rootConfig, []byte(`{"provider":{"corp":{"options":{"baseURL":"http://127.0.0.1:1/v1"}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"corp": "edit that file", "missing": "is not defined"} {
		var errOut strings.Builder
		cmd := modelsCommand{homePath: path, projectPath: rootConfig, projectTrusted: true, out: io.Discard, errOut: &errOut}
		// The valid provider listed first must not be removed either.
		if code := cmd.remove([]string{"stale", key}); code != 1 || !strings.Contains(errOut.String(), want) {
			t.Fatalf("remove %s: code = %d, stderr %q", key, code, errOut.String())
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("config changed by a refused remove:\n%s", after)
	}
}

func TestProviderProbeReasonCondensesErrors(t *testing.T) {
	for _, tc := range []struct {
		probe providerProbe
		want  string
	}{
		{providerProbe{Err: context.DeadlineExceeded}, "timed out"},
		{providerProbe{Err: io.ErrUnexpectedEOF}, "unexpected EOF"},
		{providerProbe{Err: &url.Error{Op: "Get", URL: "http://home:8001/v1/models", Err: &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errText("no route to host"))}}}, "no route to host"},
	} {
		if got := providerProbeReason(tc.probe); got != tc.want {
			t.Errorf("providerProbeReason(%v) = %q, want %q", tc.probe.Err, got, tc.want)
		}
	}
}

func TestFormatAge(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second: "<1m", 5 * time.Minute: "5m", 3 * time.Hour: "3h", 72 * time.Hour: "3d",
	} {
		if got := formatAge(d); got != want {
			t.Errorf("formatAge(%v) = %q, want %q", d, got, want)
		}
	}
}

type errText string

func (e errText) Error() string { return string(e) }
