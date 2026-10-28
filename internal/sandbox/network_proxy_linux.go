//go:build linux

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

const (
	sandboxHTTPProxyAddr  = "127.0.0.1:18080"
	sandboxSOCKSProxyAddr = "127.0.0.1:18081"
)

type linuxSandboxProxy struct {
	*sandboxNetworkController
	socketDir       string
	httpSocketPath  string
	socksSocketPath string
}

func startSandboxProxy(_ context.Context, allowlist []string) (*linuxSandboxProxy, error) {
	rules, err := parseNetworkRules(allowlist)
	if err != nil {
		return nil, err
	}
	socketDir, err := os.MkdirTemp("", "caretz-sandbox-proxy-*")
	if err != nil {
		return nil, fmt.Errorf("creating proxy socket dir: %w", err)
	}
	if err := os.Chmod(socketDir, 0700); err != nil {
		_ = os.RemoveAll(socketDir)
		return nil, fmt.Errorf("securing proxy socket dir: %w", err)
	}
	httpSocketPath := filepath.Join(socketDir, "http.sock")
	listener, err := net.Listen("unix", httpSocketPath)
	if err != nil {
		_ = os.RemoveAll(socketDir)
		return nil, fmt.Errorf("starting sandbox proxy listener: %w", err)
	}
	if err := os.Chmod(httpSocketPath, 0600); err != nil {
		_ = listener.Close()
		_ = os.RemoveAll(socketDir)
		return nil, fmt.Errorf("securing proxy socket: %w", err)
	}

	socksSocketPath := filepath.Join(socketDir, "socks.sock")
	socksListener, err := net.Listen("unix", socksSocketPath)
	if err != nil {
		_ = listener.Close()
		_ = os.RemoveAll(socketDir)
		return nil, fmt.Errorf("starting sandbox SOCKS proxy listener: %w", err)
	}
	if err := os.Chmod(socksSocketPath, 0600); err != nil {
		_ = socksListener.Close()
		_ = listener.Close()
		_ = os.RemoveAll(socketDir)
		return nil, fmt.Errorf("securing SOCKS proxy socket: %w", err)
	}
	base := &sandboxNetworkController{listener: listener, socksListener: socksListener, done: make(chan struct{}), socksDone: make(chan struct{})}
	base.authorizationPolicy = &networkAuthorizationPolicy{rules: rules, blocked: base.recordBlocked, owner: base}
	base.server = &http.Server{Handler: base.authorizationPolicy}
	proxy := &linuxSandboxProxy{sandboxNetworkController: base, socketDir: socketDir, httpSocketPath: httpSocketPath, socksSocketPath: socksSocketPath}
	go func() {
		defer close(base.done)
		if err := base.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_, _ = fmt.Fprintf(os.Stderr, "sandbox proxy error: %v\n", err)
		}
	}()
	go base.serveSOCKS()
	return proxy, nil
}

func (p *linuxSandboxProxy) Close() {
	if p == nil {
		return
	}
	p.sandboxNetworkController.Close()
	_ = os.RemoveAll(p.socketDir)
}

func startSandboxProxyBridge(addr, socketPath string) (net.Listener, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("starting sandbox proxy bridge: %w", err)
	}
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			go bridgeProxyConnection(client, socketPath)
		}
	}()
	return listener, nil
}

func bridgeProxyConnection(client net.Conn, socketPath string) {
	defer func() { _ = client.Close() }()
	upstream, err := net.Dial("unix", socketPath)
	if err != nil {
		return
	}
	defer func() { _ = upstream.Close() }()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, client)
		closeWrite(upstream)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, upstream)
		closeWrite(client)
	}()
	wg.Wait()
}
