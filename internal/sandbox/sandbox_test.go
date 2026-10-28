package sandbox

import (
	"os"
	"testing"
)

func requireSandboxIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("CARETZ_RUN_SANDBOX_INTEGRATION") != "1" {
		t.Skip("set CARETZ_RUN_SANDBOX_INTEGRATION=1 to run sandbox integration tests")
	}
}

func TestNewRunnerCopiesConfig(t *testing.T) {
	extraBinaries := []string{"/custom/bin/rg"}
	readableRoots := []string{"/custom/read"}
	writableRoots := []string{"/custom/work"}
	runner := NewRunner(Config{
		UnrestrictedNetwork: true,
		ExtraBinaries:       extraBinaries,
		ReadableRoots:       readableRoots,
		WritableRoots:       writableRoots,
	})
	extraBinaries[0] = "/custom/bin/changed"
	readableRoots[0] = "/custom/read-changed"
	writableRoots[0] = "/custom/changed"

	if !runner.config.UnrestrictedNetwork {
		t.Fatalf("expected unrestricted network config to be preserved")
	}
	if got := runner.config.ExtraBinaries[0]; got != "/custom/bin/rg" {
		t.Fatalf("expected runner to copy extra binaries, got %q", got)
	}
	if got := runner.config.ReadableRoots[0]; got != "/custom/read" {
		t.Fatalf("expected runner to copy readable roots, got %q", got)
	}
	if got := runner.config.WritableRoots[0]; got != "/custom/work" {
		t.Fatalf("expected runner to copy writable roots, got %q", got)
	}
}

func TestRunnerConfigIsolation(t *testing.T) {
	restricted := NewRunner(Config{ExtraBinaries: []string{"/custom/bin/rg"}})
	unrestricted := NewRunner(Config{
		UnrestrictedNetwork: true,
		ExtraBinaries:       []string{"/custom/bin/fd"},
	})

	if restricted.config.UnrestrictedNetwork {
		t.Fatalf("expected restricted runner to keep default network mode")
	}
	if !unrestricted.config.UnrestrictedNetwork {
		t.Fatalf("expected unrestricted runner to keep unrestricted network mode")
	}
	if restricted.config.ExtraBinaries[0] == unrestricted.config.ExtraBinaries[0] {
		t.Fatalf("expected runners to keep isolated extra binary config")
	}
}

func TestResolveTimeoutSeconds(t *testing.T) {
	tests := []struct {
		name string
		opts RunOptions
		want int
	}{
		{name: "default", opts: RunOptions{}, want: DefaultTimeoutSeconds},
		{name: "custom", opts: RunOptions{TimeoutSeconds: 300}, want: 300},
		{name: "capped", opts: RunOptions{TimeoutSeconds: MaxTimeoutSeconds + 1}, want: MaxTimeoutSeconds},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveTimeoutSeconds(tt.opts); got != tt.want {
				t.Fatalf("resolveTimeoutSeconds(%+v) = %d, want %d", tt.opts, got, tt.want)
			}
		})
	}
}
