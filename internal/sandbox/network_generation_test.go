package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestLateNetworkApprovalCannotCrossGenerations(t *testing.T) {
	for _, approvalErr := range []error{nil, errors.New("late error")} {
		t.Run(fmt.Sprint(approvalErr), func(t *testing.T) {
			proxy := &sandboxNetworkController{}
			proxy.authorizationPolicy = &networkAuthorizationPolicy{blocked: proxy.recordBlocked, owner: proxy}
			started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			old, err := proxy.beginCommandNetworkPolicy(t.Context(), nil, func(context.Context, string) (bool, error) {
				close(started)
				<-release // deliberately ignore cancellation
				return true, approvalErr
			})
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				allowed, _, err := proxy.authorizationPolicy.permit(t.Context(), "127.0.0.1:12345", "80")
				if allowed {
					done <- errors.New("stale approval allowed")
					return
				}
				done <- err
			}()
			<-started
			proxy.endCommandNetworkPolicy(old)
			current, err := proxy.beginCommandNetworkPolicy(t.Context(), []string{"127.0.0.1:12346"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer proxy.endCommandNetworkPolicy(current)
			close(release)
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("stale result: %v", err)
			}
			proxy.endCommandNetworkPolicy(old)
			if proxy.authorizationPolicy.allowed("127.0.0.1:12345", "80") || !proxy.authorizationPolicy.allowed("127.0.0.1:12346", "80") {
				t.Fatal("generation grants crossed")
			}
			if target := proxy.takeBlocked(); target != "" {
				t.Fatalf("stale denial leaked: %s", target)
			}
		})
	}
}

func TestTunnelRegistrationCannotRaceGenerationCleanup(t *testing.T) {
	for range 30 {
		proxy := &sandboxNetworkController{authorizationPolicy: &networkAuthorizationPolicy{}}
		command, err := proxy.beginCommandNetworkPolicy(t.Context(), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		local, remote := net.Pipe()
		start, done := make(chan struct{}), make(chan struct{})
		go func() { <-start; proxy.endCommandNetworkPolicy(command); close(done) }()
		close(start)
		registered := proxy.registerTunnel(command, local)
		<-done
		_ = remote.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := remote.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			t.Fatalf("stale tunnel open: %v", err)
		}
		if registered {
			proxy.unregisterTunnel(local)
		}
		_ = remote.Close()
	}
}

func TestEndingGenerationCancelsHTTPForwarding(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		close(started)
		<-req.Context().Done()
		close(canceled)
	}))
	defer upstream.Close()
	proxy, addr, _, err := startSandboxTCPProxy(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	command, err := proxy.beginCommandNetworkPolicy(t.Context(), nil, func(context.Context, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(mustParseProxyURL(t, "http://"+addr))}
	defer transport.CloseIdleConnections()
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, _ := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Get(upstream.URL)
		if resp != nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	proxy.endCommandNetworkPolicy(command)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("HTTP forwarding survived cleanup")
	}
	<-done
}

func mustParseProxyURL(t *testing.T, value string) *url.URL {
	t.Helper()
	u, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
