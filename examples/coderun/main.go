package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/makkalot/caretz"
)

func clientOptions(data string, model caretz.ModelOptions, allowPyPI bool) caretz.ClientOptions {
	options := caretz.ClientOptions{
		Model: model, WorkspaceDir: filepath.Join(data, "projects"),
		SystemPrompt: systemPrompt, ToolMode: caretz.ToolModeBuiltIns,
		State:        caretz.StateOptions{Dir: filepath.Join(data, "state")},
		Sandbox:      caretz.SandboxOptions{WritableRoots: []string{filepath.Join(data, "projects")}, AskSandboxOff: false},
		EnabledTools: map[string]bool{"Read": true, "Write": true, "Edit": true, "Ls": true, "Find": true, "Grep": true, "Bash": true},
		DisableTasks: true,
	}
	if allowPyPI {
		options.Sandbox.NetworkAllowlist = []string{"pypi.org", "files.pythonhosted.org"}
	}
	return options
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	data := flag.String("data", "./data", "Project and session storage")
	model := flag.String("model", os.Getenv("OPENAI_API_MODEL"), "Model name (OPENAI_API_MODEL; default gpt-5)")
	baseURL := flag.String("base-url", os.Getenv("OPENAI_API_BASE_URL"), "OpenAI-compatible endpoint (OPENAI_API_BASE_URL)")
	apiKey := flag.String("api-key", os.Getenv("OPENAI_API_KEY"), "API key (OPENAI_API_KEY)")
	window := flag.Int("context-window", 200000, "Model context window in tokens")
	pypi := flag.Bool("allow-pypi", false, "Preapprove PyPI access through the sandbox proxy")
	flag.Parse()
	if *window <= 0 {
		return fmt.Errorf("context-window must be positive")
	}
	if err := os.MkdirAll(*data, 0700); err != nil {
		return err
	}
	abs, err := filepath.Abs(*data)
	if err != nil {
		return err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(abs, "projects"), 0700); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, err := caretz.NewClient(ctx, clientOptions(abs, caretz.ModelOptions{
		Model: *model, BaseURL: *baseURL, APIKey: *apiKey, ContextWindow: *window,
	}, *pypi))
	if err != nil {
		return err
	}
	app, err := newApp(ctx, abs, client)
	if err != nil {
		_ = client.Close()
		return err
	}
	server := &http.Server{Addr: *addr, Handler: app.routes(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	stopped, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	log.Printf("coderun listening on http://%s", *addr)
	select {
	case <-stopped.Done():
	case err = <-serveErr:
	}
	cancel()
	app.close()
	closeErr := client.Close()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return closeErr
}
