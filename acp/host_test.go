package acp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/makkalot/caretz/acp"
)

type fakeRuntime struct {
	newServer func(acp.ServerOptions) (acp.Server, error)
}

func (r fakeRuntime) NewACPServer(options acp.ServerOptions) (acp.Server, error) {
	return r.newServer(options)
}

type echoServer struct{}

func (echoServer) ServeACP(ctx context.Context, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if _, err := output.Write(append(append([]byte(nil), scanner.Bytes()...), '\n')); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func TestHostLazilyStartsAndReusesWorkspaceEndpoint(t *testing.T) {
	host := acp.NewHost(context.Background())
	t.Cleanup(func() { _ = host.Close() })
	var starts atomic.Int32
	var gotOptions acp.ServerOptions
	err := host.RegisterWorkspace("repo", acp.WorkspaceConfig{
		Runtime: func(context.Context) (acp.Runtime, error) {
			starts.Add(1)
			return fakeRuntime{newServer: func(options acp.ServerOptions) (acp.Server, error) {
				gotOptions = options
				return echoServer{}, nil
			}}, nil
		},
		Server: acp.ServerOptions{AllowedModels: []string{"openai/gpt-test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 0 {
		t.Fatal("runtime started during registration")
	}

	first, err := host.Endpoint(context.Background(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	second, err := host.Endpoint(context.Background(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || starts.Load() != 1 {
		t.Fatalf("endpoint was not reused: first=%p second=%p starts=%d", first, second, starts.Load())
	}
	if len(gotOptions.AllowedModels) != 1 || gotOptions.AllowedModels[0] != "openai/gpt-test" {
		t.Fatalf("server options = %#v", gotOptions)
	}

	request := json.RawMessage(`{"jsonrpc":"2.0","id":"one","method":"initialize"}`)
	if err := first.Send(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-first.Messages():
		if string(got) != string(request) {
			t.Fatalf("message = %s, want %s", got, request)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ACP message")
	}
}

func TestConcurrentEndpointAcquireStartsFactoryOnce(t *testing.T) {
	host := acp.NewHost(context.Background())
	t.Cleanup(func() { _ = host.Close() })
	var starts atomic.Int32
	if err := host.RegisterWorkspace("repo", acp.WorkspaceConfig{Runtime: func(context.Context) (acp.Runtime, error) {
		starts.Add(1)
		time.Sleep(10 * time.Millisecond)
		return fakeRuntime{newServer: func(acp.ServerOptions) (acp.Server, error) { return echoServer{}, nil }}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	const callers = 16
	results := make(chan *acp.Endpoint, callers)
	errs := make(chan error, callers)
	for range callers {
		go func() {
			endpoint, err := host.Endpoint(context.Background(), "repo")
			results <- endpoint
			errs <- err
		}()
	}
	var first *acp.Endpoint
	for range callers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		endpoint := <-results
		if first == nil {
			first = endpoint
		} else if endpoint != first {
			t.Fatalf("concurrent acquire returned different endpoints: %p and %p", first, endpoint)
		}
	}
	if starts.Load() != 1 {
		t.Fatalf("runtime starts = %d, want 1", starts.Load())
	}
}

func TestEndpointRejectsInvalidJSONAndRestartsAfterClose(t *testing.T) {
	host := acp.NewHost(context.Background())
	t.Cleanup(func() { _ = host.Close() })
	var starts atomic.Int32
	if err := host.RegisterWorkspace("repo", acp.WorkspaceConfig{Runtime: func(context.Context) (acp.Runtime, error) {
		starts.Add(1)
		return fakeRuntime{newServer: func(acp.ServerOptions) (acp.Server, error) { return echoServer{}, nil }}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	endpoint, err := host.Endpoint(context.Background(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	if err := endpoint.Send(context.Background(), json.RawMessage("not-json")); !errors.Is(err, acp.ErrInvalidMessage) {
		t.Fatalf("Send error = %v", err)
	}
	if err := endpoint.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := host.Endpoint(context.Background(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	if restarted == endpoint || starts.Load() != 2 {
		t.Fatalf("endpoint did not restart: old=%p new=%p starts=%d", endpoint, restarted, starts.Load())
	}
}

func TestReplaceWorkspaceCancelsStartingFactoryAndUsesReplacement(t *testing.T) {
	host := acp.NewHost(context.Background())
	t.Cleanup(func() { _ = host.Close() })
	entered := make(chan struct{})
	var once sync.Once
	if err := host.RegisterWorkspace("repo", acp.WorkspaceConfig{Runtime: func(ctx context.Context) (acp.Runtime, error) {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return nil, ctx.Err()
	}}); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := host.Endpoint(context.Background(), "repo")
		result <- err
	}()
	<-entered
	if err := host.ReplaceWorkspace("repo", acp.WorkspaceConfig{Runtime: func(context.Context) (acp.Runtime, error) {
		return fakeRuntime{newServer: func(acp.ServerOptions) (acp.Server, error) { return echoServer{}, nil }}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("starting endpoint unexpectedly succeeded after replacement")
	}
	if _, err := host.Endpoint(context.Background(), "repo"); err != nil {
		t.Fatalf("replacement endpoint: %v", err)
	}
}

func TestRemoveWorkspaceAndCloseHost(t *testing.T) {
	host := acp.NewHost(context.Background())
	config := acp.WorkspaceConfig{Runtime: func(context.Context) (acp.Runtime, error) {
		return fakeRuntime{newServer: func(acp.ServerOptions) (acp.Server, error) { return echoServer{}, nil }}, nil
	}}
	if err := host.RegisterWorkspace("repo", config); err != nil {
		t.Fatal(err)
	}
	endpoint, err := host.Endpoint(context.Background(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	if err := host.RemoveWorkspace("repo"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-endpoint.Done():
	case <-time.After(time.Second):
		t.Fatal("removed endpoint did not close")
	}
	if _, err := host.Endpoint(context.Background(), "repo"); !errors.Is(err, acp.ErrWorkspaceNotFound) {
		t.Fatalf("Endpoint after remove error = %v", err)
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if err := host.RegisterWorkspace("other", config); !errors.Is(err, acp.ErrHostClosed) {
		t.Fatalf("RegisterWorkspace after close error = %v", err)
	}
}

func TestConcurrentHostCloseWaitsForEndpointShutdown(t *testing.T) {
	host := acp.NewHost(context.Background())
	release := make(chan struct{})
	server := blockingCloseServer{release: release}
	if err := host.RegisterWorkspace("repo", acp.WorkspaceConfig{Runtime: func(context.Context) (acp.Runtime, error) {
		return fakeRuntime{newServer: func(acp.ServerOptions) (acp.Server, error) { return server, nil }}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Endpoint(context.Background(), "repo"); err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- host.Close() }()
	go func() { second <- host.Close() }()
	select {
	case err := <-second:
		t.Fatalf("second Close returned before shutdown: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

type blockingCloseServer struct {
	release <-chan struct{}
}

func (s blockingCloseServer) ServeACP(ctx context.Context, _ io.Reader, _ io.Writer) error {
	<-ctx.Done()
	<-s.release
	return nil
}
