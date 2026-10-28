package sandbox

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAllowlistProxyReportsBlockedTargetAndAcceptsTemporaryGrant(t *testing.T) {
	proxy := &sandboxNetworkController{}
	policy := &networkAuthorizationPolicy{blocked: proxy.recordBlocked}
	proxy.authorizationPolicy = policy
	_, _ = proxy.beginCommandNetworkPolicy(t.Context(), nil, nil)

	request := httptest.NewRequest(http.MethodGet, "https://example.com/resource", nil)
	recorder := httptest.NewRecorder()
	policy.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if target := proxy.takeBlocked(); target != "example.com:443" {
		t.Fatalf("blocked target = %q", target)
	}
	if _, err := proxy.beginCommandNetworkPolicy(t.Context(), []string{"example.com:443"}, nil); err != nil {
		t.Fatal(err)
	}
	if !policy.allowed("example.com", "443") {
		t.Fatal("temporary exact host:port grant was not applied")
	}
	if policy.allowed("example.com", "80") {
		t.Fatal("temporary grant leaked to another port")
	}
}

func TestAllowlistProxyLiveLoopbackApprovalIsExactAndDeduplicated(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	policy := &networkAuthorizationPolicy{}
	proxy := &sandboxNetworkController{authorizationPolicy: policy}
	if _, err := proxy.beginCommandNetworkPolicy(t.Context(), nil, func(context.Context, string) (bool, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}

	const requests = 5
	var wg sync.WaitGroup
	errs := make(chan error, requests)
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed, temporary, err := policy.permit(context.Background(), "127.0.0.1:43127", "80")
			if err != nil {
				errs <- err
				return
			}
			if !allowed || !temporary {
				errs <- fmt.Errorf("permit = (%t, %t), want (true, true)", allowed, temporary)
			}
		}()
	}
	<-started
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("authorization calls = %d, want 1", calls.Load())
	}
	if policy.allowed("127.0.0.1:43128", "80") {
		t.Fatal("exact approval leaked to a different localhost port")
	}
}

func TestAllowlistProxyDeniedLiveApprovalDoesNotQueueSecondPrompt(t *testing.T) {
	proxy := &sandboxNetworkController{}
	policy := &networkAuthorizationPolicy{blocked: proxy.recordBlocked}
	proxy.authorizationPolicy = policy
	if _, err := proxy.beginCommandNetworkPolicy(t.Context(), nil, func(context.Context, string) (bool, error) { return false, nil }); err != nil {
		t.Fatal(err)
	}
	allowed, _, err := policy.permit(context.Background(), "localhost:43127", "80")
	if err != nil || allowed {
		t.Fatalf("permit = (%t, %v), want denied", allowed, err)
	}
	if target := proxy.takeBlocked(); target != "" {
		t.Fatalf("denied live approval queued another prompt for %q", target)
	}
}

func TestSandboxProxyCloseTerminatesConnectTunnel(t *testing.T) {
	upstream, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upstream.Close() }()
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		conn, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = io.Copy(conn, conn)
	}()

	proxy, proxyAddr, _, err := startSandboxTCPProxy(t.Context(), []string{upstream.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		proxy.Close()
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("CONNECT " + upstream.Addr().String() + " HTTP/1.1\r\nHost: " + upstream.Addr().String() + "\r\n\r\n")); err != nil {
		proxy.Close()
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		proxy.Close()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		proxy.Close()
		t.Fatalf("CONNECT status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		proxy.Close()
		t.Fatal(err)
	}
	echo := make([]byte, 4)
	if _, err := io.ReadFull(reader, echo); err != nil || string(echo) != "ping" {
		proxy.Close()
		t.Fatalf("tunnel echo = %q, err=%v", echo, err)
	}

	proxy.Close()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("CONNECT tunnel remained readable after proxy close")
	}
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
		t.Fatal("upstream CONNECT connection remained open after proxy close")
	}
}

func TestClearingCommandPolicyTerminatesTemporaryConnectTunnel(t *testing.T) {
	upstream, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upstream.Close() }()
	upstreamDone := make(chan struct{})
	go func() {
		defer close(upstreamDone)
		conn, acceptErr := upstream.Accept()
		if acceptErr == nil {
			_, _ = io.Copy(io.Discard, conn)
			_ = conn.Close()
		}
	}()

	proxy, proxyAddr, _, err := startSandboxTCPProxy(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	commandPolicy, err := proxy.beginCommandNetworkPolicy(t.Context(), nil, func(context.Context, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte("CONNECT " + upstream.Addr().String() + " HTTP/1.1\r\nHost: " + upstream.Addr().String() + "\r\n\r\n"))
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT response = %#v, %v", resp, err)
	}
	proxy.endCommandNetworkPolicy(commandPolicy)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("temporary CONNECT tunnel survived command policy cleanup")
	}
	select {
	case <-upstreamDone:
	case <-time.After(time.Second):
		t.Fatal("temporary upstream connection survived command policy cleanup")
	}
}

func TestParseNetworkRule(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		host      string
		port      string
		protocol  string
		wildcard  bool
		anyHost   bool
		wantError bool
	}{
		{name: "host", value: "Example.COM.", host: "example.com"},
		{name: "host port", value: "example.com:443", host: "example.com", port: "443"},
		{name: "wildcard", value: "*.example.com", host: "example.com", wildcard: true},
		{name: "any host with port", value: "*:443", port: "443", anyHost: true},
		{name: "tcp host", value: "tcp://Example.COM.:22", host: "example.com", port: "22", protocol: "tcp"},
		{name: "tcp any host", value: "tcp://*:22", port: "22", protocol: "tcp", anyHost: true},
		{name: "tcp requires port", value: "tcp://example.com", wantError: true},
		{name: "scheme rejected", value: "https://example.com", wantError: true},
		{name: "bad wildcard rejected", value: "api.*.example.com", wantError: true},
		{name: "any host without port rejected", value: "*", wantError: true},
		{name: "any host with missing port rejected", value: "*:", wantError: true},
		{name: "any host with zero port rejected", value: "*:0", wantError: true},
		{name: "any host with high port rejected", value: "*:65536", wantError: true},
		{name: "bad port rejected", value: "example.com:99999", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseNetworkRule(tt.value)
			if tt.wantError {
				if err == nil {
					t.Fatalf("parseNetworkRule(%q) error = nil, want error", tt.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseNetworkRule(%q) error = %v", tt.value, err)
			}
			if got.protocol != tt.protocol || got.host != tt.host || got.port != tt.port || got.wildcard != tt.wildcard || got.anyHost != tt.anyHost {
				t.Fatalf("parseNetworkRule(%q) = %#v, want host=%q port=%q wildcard=%v anyHost=%v", tt.value, got, tt.host, tt.port, tt.wildcard, tt.anyHost)
			}
		})
	}
}

func TestHostAllowed(t *testing.T) {
	rules, err := parseNetworkRules([]string{"example.com", "*.example.org", "ports.test:8443", "*:9443"})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		host string
		port string
		want bool
	}{
		{host: "example.com", port: "443", want: true},
		{host: "EXAMPLE.com:443", port: "80", want: true},
		{host: "api.example.org", port: "443", want: true},
		{host: "example.org", port: "443", want: false},
		{host: "ports.test:8443", port: "443", want: true},
		{host: "ports.test:443", port: "443", want: false},
		{host: "blocked.test", port: "443", want: false},
		{host: "anything.test:9443", port: "443", want: true},
		{host: "127.0.0.1:9443", port: "443", want: true},
		{host: "[::1]:9443", port: "443", want: true},
		{host: "anything.test:9444", port: "443", want: false},
	}

	for _, tt := range tests {
		if got := hostAllowed(rules, "", tt.host, tt.port); got != tt.want {
			t.Fatalf("hostAllowed(%q, %q) = %v, want %v", tt.host, tt.port, got, tt.want)
		}
	}
}

func TestSOCKSConnectRelaysAllowedTCPAndScopesRules(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upstream.Close() }()
	go func() {
		conn, acceptErr := upstream.Accept()
		if acceptErr == nil {
			defer func() { _ = conn.Close() }()
			_, _ = io.Copy(conn, conn)
		}
	}()
	proxy, _, socksAddr, err := startSandboxTCPProxy(t.Context(), []string{"tcp://" + upstream.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	conn := openSOCKSTunnel(t, socksAddr, upstream.Addr().String())
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("SOCKS relay = %q, %v", buf, err)
	}
	rules, _ := parseNetworkRules([]string{upstream.Addr().String()})
	if hostAllowed(rules, "tcp", upstream.Addr().String(), "") {
		t.Fatal("legacy HTTP rule authorized SOCKS TCP")
	}
	rules, _ = parseNetworkRules([]string{"tcp://" + upstream.Addr().String()})
	if hostAllowed(rules, "", upstream.Addr().String(), "") {
		t.Fatal("tcp:// rule authorized HTTP")
	}
}

func TestSOCKSConnectSupportsDomainAndIPv6Targets(t *testing.T) {
	t.Run("domain", func(t *testing.T) {
		upstream := startTCPEchoServer(t, "tcp4", "127.0.0.1:0")
		_, port, _ := net.SplitHostPort(upstream.Addr().String())
		proxy, _, socksAddr, err := startSandboxTCPProxy(t.Context(), []string{"tcp://localhost:" + port})
		if err != nil {
			t.Fatal(err)
		}
		defer proxy.Close()
		conn := openSOCKSTunnelHost(t, socksAddr, "localhost", port)
		defer func() { _ = conn.Close() }()
		assertTunnelEcho(t, conn)
	})

	t.Run("ipv6", func(t *testing.T) {
		upstream, err := net.Listen("tcp6", "[::1]:0")
		if err != nil {
			t.Skipf("IPv6 loopback unavailable: %v", err)
		}
		defer func() { _ = upstream.Close() }()
		serveEcho(upstream)
		host, port, _ := net.SplitHostPort(upstream.Addr().String())
		proxy, _, socksAddr, err := startSandboxTCPProxy(t.Context(), []string{"tcp://" + upstream.Addr().String()})
		if err != nil {
			t.Fatal(err)
		}
		defer proxy.Close()
		conn := openSOCKSTunnelHost(t, socksAddr, host, port)
		defer func() { _ = conn.Close() }()
		assertTunnelEcho(t, conn)
	})
}

func TestSOCKSRelayPreservesHalfClose(t *testing.T) {
	upstream, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upstream.Close() }()
	go func() {
		conn, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		body, _ := io.ReadAll(conn)
		_, _ = conn.Write(append([]byte("received:"), body...))
	}()
	proxy, _, socksAddr, err := startSandboxTCPProxy(t.Context(), []string{"tcp://" + upstream.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	conn := openSOCKSTunnel(t, socksAddr, upstream.Addr().String())
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		t.Fatalf("SOCKS connection type = %T, want *net.TCPConn", conn)
	}
	if err := tcpConn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	response, err := io.ReadAll(conn)
	if err != nil || string(response) != "received:payload" {
		t.Fatalf("half-close response = %q, %v", response, err)
	}
}

func TestSOCKSTemporaryApprovalEndsWithCommand(t *testing.T) {
	upstream := startTCPEchoServer(t, "tcp4", "127.0.0.1:0")
	proxy, _, socksAddr, err := startSandboxTCPProxy(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	command, err := proxy.beginCommandNetworkPolicy(t.Context(), nil, func(_ context.Context, target string) (bool, error) {
		return target == "tcp://"+upstream.Addr().String(), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	conn := openSOCKSTunnel(t, socksAddr, upstream.Addr().String())
	assertTunnelEcho(t, conn)
	proxy.endCommandNetworkPolicy(command)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("temporary SOCKS tunnel survived command cleanup")
	}

	next, err := proxy.beginCommandNetworkPolicy(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.endCommandNetworkPolicy(next)
	assertSOCKSConnectStatus(t, socksAddr, upstream.Addr().String(), 2)
}

func TestSOCKSApprovalOutlivesClientDisconnectUntilCommandEnds(t *testing.T) {
	proxy, _, socksAddr, err := startSandboxTCPProxy(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	asked := make(chan struct{})
	cancelled := make(chan struct{})
	command, err := proxy.beginCommandNetworkPolicy(t.Context(), nil, func(ctx context.Context, _ string) (bool, error) {
		close(asked)
		<-ctx.Done()
		close(cancelled)
		return false, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.endCommandNetworkPolicy(command)
	conn, err := net.Dial("tcp", socksAddr)
	if err != nil {
		t.Fatal(err)
	}
	socksGreeting(t, conn)
	_, _ = conn.Write([]byte{5, 1, 0, 1, 127, 0, 0, 1, 0, 22})
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("loopback SOCKS target was not sent for approval")
	}
	_ = conn.Close()
	select {
	case <-cancelled:
		t.Fatal("approval was cancelled when the SOCKS client disconnected")
	case <-time.After(100 * time.Millisecond):
	}
	proxy.endCommandNetworkPolicy(command)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("approval stayed pending after the command ended")
	}
}

func TestLiveApprovalSurvivesClientTimeoutAndRetry(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()
	proxy, proxyAddr, _, err := startSandboxTCPProxy(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	var calls atomic.Int32
	asked := make(chan struct{})
	release := make(chan struct{})
	command, err := proxy.beginCommandNetworkPolicy(t.Context(), nil, func(ctx context.Context, _ string) (bool, error) {
		if calls.Add(1) == 1 {
			close(asked)
		}
		select {
		case <-release:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.endCommandNetworkPolicy(command)
	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 200 * time.Millisecond}

	// A test client with a short timeout gives up while the user reads the prompt.
	if response, err := client.Get(upstream.URL); err == nil {
		_ = response.Body.Close()
		t.Fatal("request succeeded before approval")
	}
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("loopback target was not sent for approval")
	}

	// Its retry joins the open prompt, and approving it lets the retry through.
	client.Timeout = 5 * time.Second
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(release)
	}()
	response, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatalf("retry after approval: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("retry = %d %q", response.StatusCode, body)
	}
	if calls.Load() != 1 {
		t.Fatalf("authorization calls = %d, want 1", calls.Load())
	}
}

func TestSOCKSForwardsBytesSentDuringApproval(t *testing.T) {
	upstream := startTCPEchoServer(t, "tcp4", "127.0.0.1:0")
	proxy, _, socksAddr, err := startSandboxTCPProxy(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	release := make(chan struct{})
	command, err := proxy.beginCommandNetworkPolicy(t.Context(), nil, func(context.Context, string) (bool, error) {
		<-release
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.endCommandNetworkPolicy(command)
	conn, err := net.Dial("tcp", socksAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	socksGreeting(t, conn)
	host, portText, _ := net.SplitHostPort(upstream.Addr().String())
	port, _ := strconv.Atoi(portText)
	request := append([]byte{5, 1, 0, 1}, net.ParseIP(host).To4()...)
	request = append(request, byte(port>>8), byte(port))
	// Optimistic data sent before the reply must reach the target intact.
	_, _ = conn.Write(append(request, "early"...))
	time.Sleep(50 * time.Millisecond)
	close(release)
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0 {
		t.Fatalf("SOCKS CONNECT reply = %v, %v", reply, err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	echo := make([]byte, len("early"))
	if _, err := io.ReadFull(conn, echo); err != nil || string(echo) != "early" {
		t.Fatalf("early bytes = %q, %v", echo, err)
	}
}

func TestSandboxProxyCloseTerminatesSOCKSTunnel(t *testing.T) {
	upstream := startTCPEchoServer(t, "tcp4", "127.0.0.1:0")
	proxy, _, socksAddr, err := startSandboxTCPProxy(t.Context(), []string{"tcp://" + upstream.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	conn := openSOCKSTunnel(t, socksAddr, upstream.Addr().String())
	assertTunnelEcho(t, conn)
	proxy.Close()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("SOCKS tunnel remained open after gateway shutdown")
	}
}

func TestSOCKSCommandCancellationTerminatesTemporaryTunnel(t *testing.T) {
	upstream := startTCPEchoServer(t, "tcp4", "127.0.0.1:0")
	proxy, _, socksAddr, err := startSandboxTCPProxy(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	ctx, cancel := context.WithCancel(t.Context())
	if _, err := proxy.beginCommandNetworkPolicy(ctx, nil, func(context.Context, string) (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	conn := openSOCKSTunnel(t, socksAddr, upstream.Addr().String())
	assertTunnelEcho(t, conn)
	cancel()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("temporary SOCKS tunnel survived command cancellation")
	}
}

func TestSOCKSBlockedTargetIsProtocolAware(t *testing.T) {
	proxy, _, socksAddr, err := startSandboxTCPProxy(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	command, _ := proxy.beginCommandNetworkPolicy(t.Context(), nil, nil)
	defer proxy.endCommandNetworkPolicy(command)
	conn, err := net.Dial("tcp", socksAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	socksGreeting(t, conn)
	_, _ = conn.Write([]byte{5, 1, 0, 3, 11, 'e', 'x', 'a', 'm', 'p', 'l', 'e', '.', 'c', 'o', 'm', 0, 22})
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 2 {
		t.Fatalf("SOCKS reply = %v, %v", reply, err)
	}
	if got := proxy.takeBlocked(); got != "tcp://example.com:22" {
		t.Fatalf("blocked target = %q", got)
	}
}

func TestSOCKSRejectsAuthenticationBINDUDPAndMalformedRequests(t *testing.T) {
	proxy, _, socksAddr, err := startSandboxTCPProxy(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	conn, err := net.Dial("tcp", socksAddr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte{5, 1, 2})
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0xff {
		t.Fatalf("auth reply = %v, %v", reply, err)
	}
	_ = conn.Close()
	for _, command := range []byte{2, 3} {
		conn, _ = net.Dial("tcp", socksAddr)
		socksGreeting(t, conn)
		_, _ = conn.Write([]byte{5, command, 0, 1, 127, 0, 0, 1, 0, 53})
		fullReply := make([]byte, 10)
		if _, err := io.ReadFull(conn, fullReply); err != nil || fullReply[1] != 7 {
			t.Fatalf("command %d reply = %v, %v", command, fullReply, err)
		}
		_ = conn.Close()
	}
	for _, request := range [][]byte{
		{4, 1, 0, 1, 127, 0, 0, 1, 0, 80},
		{5, 1, 1, 1, 127, 0, 0, 1, 0, 80},
		{5, 1, 0, 9},
		{5, 1, 0, 3, 0},
	} {
		conn, _ = net.Dial("tcp", socksAddr)
		socksGreeting(t, conn)
		_, _ = conn.Write(request)
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_, _ = io.ReadAll(conn)
		_ = conn.Close()
	}
}

func openSOCKSTunnel(t *testing.T, proxyAddr, target string) net.Conn {
	t.Helper()
	host, port, _ := net.SplitHostPort(target)
	return openSOCKSTunnelHost(t, proxyAddr, host, port)
}

func openSOCKSTunnelHost(t *testing.T, proxyAddr, host, portText string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	socksGreeting(t, conn)
	port, _ := strconv.Atoi(portText)
	request := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		request = append(request, 1)
		request = append(request, ip.To4()...)
	} else if ip != nil {
		request = append(request, 4)
		request = append(request, ip.To16()...)
	} else {
		request = append(request, 3, byte(len(host)))
		request = append(request, host...)
	}
	request = append(request, byte(port>>8), byte(port))
	_, _ = conn.Write(request)
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0 {
		_ = conn.Close()
		t.Fatalf("SOCKS CONNECT reply = %v, %v", reply, err)
	}
	return conn
}

func assertSOCKSConnectStatus(t *testing.T, proxyAddr, target string, want byte) {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	socksGreeting(t, conn)
	host, portText, _ := net.SplitHostPort(target)
	port, _ := strconv.Atoi(portText)
	request := append([]byte{5, 1, 0, 1}, net.ParseIP(host).To4()...)
	request = append(request, byte(port>>8), byte(port))
	_, _ = conn.Write(request)
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != want {
		t.Fatalf("SOCKS CONNECT reply = %v, %v; want status %d", reply, err, want)
	}
}

func startTCPEchoServer(t *testing.T, network, addr string) net.Listener {
	t.Helper()
	listener, err := net.Listen(network, addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serveEcho(listener)
	return listener
}

func serveEcho(listener net.Listener) {
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
}

func assertTunnelEcho(t *testing.T, conn net.Conn) {
	t.Helper()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("SOCKS relay = %q, %v", buf, err)
	}
}

func socksGreeting(t *testing.T, conn net.Conn) {
	t.Helper()
	_, _ = conn.Write([]byte{5, 1, 0})
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[0] != 5 || reply[1] != 0 {
		t.Fatalf("SOCKS greeting = %v, %v", reply, err)
	}
}

func TestAllowlistProxyAllowsHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	proxy := httptest.NewServer(&networkAuthorizationPolicy{rules: []networkRule{{host: upstreamURL.Hostname(), port: upstreamURL.Port()}}})
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", string(body))
	}
}

func TestAllowlistProxyAllowsAnyHostOnConfiguredHTTPPort(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	rule, err := parseNetworkRule("*:" + upstreamURL.Port())
	if err != nil {
		t.Fatal(err)
	}

	proxy := httptest.NewServer(&networkAuthorizationPolicy{rules: []networkRule{rule}})
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestAllowlistProxyBlocksAnyHostOnDifferentHTTPPort(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("should not reach"))
	}))
	defer upstream.Close()
	rule, err := parseNetworkRule("*:1")
	if err != nil {
		t.Fatal(err)
	}

	proxy := httptest.NewServer(&networkAuthorizationPolicy{rules: []networkRule{rule}})
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestAllowlistProxyBlocksHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("should not reach"))
	}))
	defer upstream.Close()

	proxy := httptest.NewServer(&networkAuthorizationPolicy{rules: []networkRule{{host: "allowed.test"}}})
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestAllowlistProxyAllowsConnect(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upstream.Close() }()
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = io.Copy(conn, conn)
	}()

	_, port, err := net.SplitHostPort(upstream.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	rule, err := parseNetworkRule("*:" + port)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(&networkAuthorizationPolicy{rules: []networkRule{rule}})
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyConn, err := net.Dial("tcp", proxyURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proxyConn.Close() }()

	_, err = proxyConn.Write([]byte("CONNECT " + upstream.Addr().String() + " HTTP/1.1\r\nHost: " + upstream.Addr().String() + "\r\n\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(proxyConn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	_, err = proxyConn.Write([]byte("ping"))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("tunnel echo = %q, want ping", string(buf))
	}
}
