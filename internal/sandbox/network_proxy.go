package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type networkRule struct {
	protocol string
	host     string
	wildcard bool
	anyHost  bool
	port     string
}

type sandboxNetworkController struct {
	server              *http.Server
	listener            net.Listener
	socksListener       net.Listener
	done                chan struct{}
	socksDone           chan struct{}
	closeOnce           sync.Once
	tunnelMu            sync.Mutex
	tunnels             map[net.Conn]*commandNetworkPolicy
	closing             bool
	tunnelWG            sync.WaitGroup
	blockedMu           sync.Mutex
	blocked             []string
	authorizationPolicy *networkAuthorizationPolicy
}

type NetworkBlockedError struct {
	Target string
}

func (networkError *NetworkBlockedError) Error() string {
	return "sandbox network blocked " + networkError.Target
}

func (networkController *sandboxNetworkController) recordBlocked(target string) {
	networkController.blockedMu.Lock()
	for _, existing := range networkController.blocked {
		if existing == target {
			networkController.blockedMu.Unlock()
			return
		}
	}
	networkController.blocked = append(networkController.blocked, target)
	networkController.blockedMu.Unlock()
}

func (networkController *sandboxNetworkController) takeBlocked() string {
	networkController.blockedMu.Lock()
	defer networkController.blockedMu.Unlock()
	if len(networkController.blocked) == 0 {
		return ""
	}
	target := networkController.blocked[0]
	networkController.blocked = nil
	return target
}

func (networkController *sandboxNetworkController) beginCommandNetworkPolicy(ctx context.Context, values []string, authorize func(context.Context, string) (bool, error)) (*commandNetworkPolicy, error) {
	rules, err := parseNetworkRules(values)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	networkController.authorizationPolicy.mu.Lock()
	networkController.tunnelMu.Lock()
	closing := networkController.closing
	networkController.tunnelMu.Unlock()
	if closing {
		networkController.authorizationPolicy.mu.Unlock()
		cancel()
		return nil, fmt.Errorf("sandbox networkController is closed")
	}
	previousCommand := networkController.authorizationPolicy.command
	networkController.authorizationPolicy.nextGeneration++
	command := &commandNetworkPolicy{generation: networkController.authorizationPolicy.nextGeneration, ctx: ctx, cancel: cancel, temporary: rules, authorize: authorize, pending: make(map[string]*networkAuthorization)}
	networkController.authorizationPolicy.command = command
	networkController.blockedMu.Lock()
	networkController.blocked = nil
	networkController.blockedMu.Unlock()
	networkController.authorizationPolicy.mu.Unlock()
	if previousCommand != nil {
		previousCommand.cancel()
		networkController.closeCommandTunnels(previousCommand)
	}
	context.AfterFunc(ctx, func() { networkController.endCommandNetworkPolicy(command) })
	return command, nil
}

func (networkController *sandboxNetworkController) endCommandNetworkPolicy(command *commandNetworkPolicy) {
	if command == nil {
		return
	}
	networkController.authorizationPolicy.mu.Lock()
	if networkController.authorizationPolicy.command == command {
		networkController.authorizationPolicy.command = nil
	}
	command.cancel()
	networkController.authorizationPolicy.mu.Unlock()
	networkController.closeCommandTunnels(command)
}

func startSandboxTCPProxy(_ context.Context, networkRules []string) (*sandboxNetworkController, string, string, error) {
	rules, err := parseNetworkRules(networkRules)
	if err != nil {
		return nil, "", "", err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, "", "", fmt.Errorf("starting sandbox HTTP proxy listener: %w", err)
	}
	socksListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		_ = listener.Close()
		return nil, "", "", fmt.Errorf("starting sandbox SOCKS proxy listener: %w", err)
	}
	networkController := &sandboxNetworkController{listener: listener, socksListener: socksListener, done: make(chan struct{}), socksDone: make(chan struct{})}
	networkController.authorizationPolicy = &networkAuthorizationPolicy{rules: rules, blocked: networkController.recordBlocked, owner: networkController}
	networkController.server = &http.Server{Handler: networkController.authorizationPolicy}
	go func() {
		defer close(networkController.done)
		if err := networkController.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_, _ = fmt.Fprintf(os.Stderr, "sandbox networkController error: %v\n", err)
		}
	}()
	go networkController.serveSOCKS()
	return networkController, listener.Addr().String(), socksListener.Addr().String(), nil
}

func (networkController *sandboxNetworkController) Close() {
	if networkController == nil {
		return
	}
	networkController.closeOnce.Do(func() {
		networkController.authorizationPolicy.mu.Lock()
		command := networkController.authorizationPolicy.command
		networkController.authorizationPolicy.command = nil
		if command != nil {
			command.cancel()
		}
		networkController.closeTunnels()
		networkController.authorizationPolicy.mu.Unlock()
		if networkController.server != nil {
			_ = networkController.server.Close()
		}
		if networkController.listener != nil {
			_ = networkController.listener.Close()
		}
		if networkController.socksListener != nil {
			_ = networkController.socksListener.Close()
		}
		<-networkController.done
		if networkController.socksDone != nil {
			<-networkController.socksDone
		}
		networkController.tunnelWG.Wait()
	})
}

func (networkController *sandboxNetworkController) registerTunnel(command *commandNetworkPolicy, conns ...net.Conn) bool {
	// Lock order is authorizationPolicy then tunnels, so invalidation and registration agree
	// on whether this generation is still permitted to create a tunnel.
	networkController.authorizationPolicy.mu.RLock()
	defer networkController.authorizationPolicy.mu.RUnlock()
	networkController.tunnelMu.Lock()
	if networkController.closing || command != nil && (networkController.authorizationPolicy.command != command || command.ctx.Err() != nil) {
		networkController.tunnelMu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		return false
	}
	if networkController.tunnels == nil {
		networkController.tunnels = make(map[net.Conn]*commandNetworkPolicy)
	}
	for _, conn := range conns {
		networkController.tunnels[conn] = command
	}
	networkController.tunnelWG.Add(1)
	networkController.tunnelMu.Unlock()
	return true
}

func (networkController *sandboxNetworkController) closeCommandTunnels(command *commandNetworkPolicy) {
	networkController.tunnelMu.Lock()
	var conns []net.Conn
	for conn, scope := range networkController.tunnels {
		if scope == command {
			conns = append(conns, conn)
		}
	}
	networkController.tunnelMu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (networkController *sandboxNetworkController) unregisterTunnel(conns ...net.Conn) {
	networkController.tunnelMu.Lock()
	for _, conn := range conns {
		delete(networkController.tunnels, conn)
	}
	networkController.tunnelMu.Unlock()
	networkController.tunnelWG.Done()
}

func (networkController *sandboxNetworkController) rescopeTunnel(conn net.Conn, command *commandNetworkPolicy) bool {
	networkController.authorizationPolicy.mu.RLock()
	defer networkController.authorizationPolicy.mu.RUnlock()
	networkController.tunnelMu.Lock()
	defer networkController.tunnelMu.Unlock()
	if networkController.closing {
		return false
	}
	if command != nil && (networkController.authorizationPolicy.command != command || command.ctx.Err() != nil) {
		return false
	}
	if _, tracked := networkController.tunnels[conn]; !tracked {
		return false
	}
	networkController.tunnels[conn] = command
	return true
}

func (networkController *sandboxNetworkController) closeTunnels() {
	networkController.tunnelMu.Lock()
	networkController.closing = true
	conns := make([]net.Conn, 0, len(networkController.tunnels))
	for conn := range networkController.tunnels {
		conns = append(conns, conn)
	}
	networkController.tunnelMu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

type networkAuthorizationPolicy struct {
	mu             sync.RWMutex
	rules          []networkRule
	command        *commandNetworkPolicy
	nextGeneration uint64
	transport      http.RoundTripper
	blocked        func(string)
	owner          *sandboxNetworkController
}

type commandNetworkPolicy struct {
	generation uint64
	ctx        context.Context
	cancel     context.CancelFunc
	temporary  []networkRule
	authorize  func(context.Context, string) (bool, error)
	pending    map[string]*networkAuthorization
}

type networkAuthorization struct {
	done    chan struct{}
	allowed bool
	err     error
}

func (authorizationPolicy *networkAuthorizationPolicy) allowed(host, defaultPort string) bool {
	allowed, _ := authorizationPolicy.allowedScope("", host, defaultPort)
	return allowed
}

func (authorizationPolicy *networkAuthorizationPolicy) allowedScope(protocol, host, defaultPort string) (bool, bool) {
	authorizationPolicy.mu.RLock()
	base := hostAllowed(authorizationPolicy.rules, protocol, host, defaultPort)
	temporary := !base && authorizationPolicy.command != nil && authorizationPolicy.command.ctx.Err() == nil && hostAllowed(authorizationPolicy.command.temporary, protocol, host, defaultPort)
	authorizationPolicy.mu.RUnlock()
	return base || temporary, temporary
}

func (authorizationPolicy *networkAuthorizationPolicy) permit(ctx context.Context, host, defaultPort string) (bool, bool, error) {
	allowed, command, err := authorizationPolicy.permitScoped(ctx, "", host, defaultPort)
	return allowed, command != nil, err
}

func networkRequestContext(request context.Context, command *commandNetworkPolicy) (context.Context, func()) {
	ctx, cancel := context.WithCancel(request)
	if command == nil {
		return ctx, cancel
	}
	if deadline, deadlineAvailable := command.ctx.Deadline(); deadlineAvailable {
		cancel()
		ctx, cancel = context.WithDeadline(request, deadline)
	}
	stop := context.AfterFunc(command.ctx, cancel)
	if command.ctx.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

func (authorizationPolicy *networkAuthorizationPolicy) permitScoped(ctx context.Context, protocol, host, defaultPort string) (bool, *commandNetworkPolicy, error) {
	target := canonicalHostPort(host, defaultPort)
	if protocol != "" {
		target = protocol + "://" + target
	}
	authorizationPolicy.mu.Lock()
	if hostAllowed(authorizationPolicy.rules, protocol, host, defaultPort) {
		authorizationPolicy.mu.Unlock()
		return true, nil, nil
	}
	command := authorizationPolicy.command
	if command != nil && command.ctx.Err() != nil {
		authorizationPolicy.mu.Unlock()
		return false, nil, command.ctx.Err()
	}
	if command != nil && hostAllowed(command.temporary, protocol, host, defaultPort) {
		authorizationPolicy.mu.Unlock()
		return true, command, nil
	}
	if command == nil || command.authorize == nil || !isLoopbackTarget(canonicalHostPort(host, defaultPort)) {
		if authorizationPolicy.blocked != nil && (command != nil || authorizationPolicy.owner == nil) {
			authorizationPolicy.blocked(target)
		}
		authorizationPolicy.mu.Unlock()
		return false, nil, nil
	}
	pending := command.pending[target]
	if pending == nil {
		pending = &networkAuthorization{done: make(chan struct{})}
		command.pending[target] = pending
		// The prompt outlives the connection that raised it: clients that
		// time out and retry join it instead of cancelling it and asking again.
		go authorizationPolicy.authorizeTarget(command, target, pending)
	}
	authorizationPolicy.mu.Unlock()

	requestCtx, cleanup := networkRequestContext(ctx, command)
	defer cleanup()
	select {
	case <-pending.done:
		if err := requestCtx.Err(); err != nil {
			return false, nil, err
		}
		return pending.allowed, command, pending.err
	case <-requestCtx.Done():
		return false, nil, requestCtx.Err()
	}
}

// authorizeTarget asks once per command and target, for as long as the
// command runs, and publishes the answer to every waiting connection.
func (authorizationPolicy *networkAuthorizationPolicy) authorizeTarget(command *commandNetworkPolicy, target string, pending *networkAuthorization) {
	allowed, err := command.authorize(command.ctx, target)
	authorizationPolicy.mu.Lock()
	if authorizationPolicy.command != command || command.ctx.Err() != nil {
		allowed, err = false, context.Canceled
	} else if err != nil && authorizationPolicy.blocked != nil {
		authorizationPolicy.blocked(target)
	}
	if allowed && err == nil {
		rule, parseErr := parseNetworkRule(target)
		if parseErr != nil {
			err = parseErr
			allowed = false
		} else {
			command.temporary = append(command.temporary, rule)
		}
	}
	pending.allowed = allowed
	pending.err = err
	delete(command.pending, target)
	close(pending.done)
	authorizationPolicy.mu.Unlock()
}

func (authorizationPolicy *networkAuthorizationPolicy) ServeHTTP(responseWriter http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodConnect {
		authorizationPolicy.handleConnect(responseWriter, request)
		return
	}
	if request.URL == nil || request.URL.Scheme == "" || request.URL.Host == "" {
		http.Error(responseWriter, "networkController requires absolute HTTP URL", http.StatusBadRequest)
		return
	}
	if request.URL.Scheme != "http" && request.URL.Scheme != "https" {
		http.Error(responseWriter, "networkController only supports HTTP and HTTPS", http.StatusForbidden)
		return
	}
	allowed, command, err := authorizationPolicy.permitScoped(request.Context(), "", request.URL.Host, defaultPort(request.URL.Scheme))
	if err != nil {
		http.Error(responseWriter, "sandbox network authorization failed", http.StatusBadGateway)
		return
	}
	if !allowed {
		http.Error(responseWriter, "sandbox networkController blocked host", http.StatusForbidden)
		return
	}

	requestCtx, cleanup := networkRequestContext(request.Context(), command)
	defer cleanup()
	if requestCtx.Err() != nil {
		return
	}
	outReq := request.Clone(requestCtx)
	outReq.RequestURI = ""
	outReq.URL = cloneURL(request.URL)
	outReq.Header = request.Header.Clone()
	outReq.Header.Del("Proxy-Connection")

	transport := authorizationPolicy.transport
	if transport == nil {
		transport = directProxyTransport()
	}
	resp, err := transport.RoundTrip(outReq)
	if err != nil {
		http.Error(responseWriter, err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	copyHeader(responseWriter.Header(), resp.Header)
	responseWriter.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(responseWriter, resp.Body)
}

func (authorizationPolicy *networkAuthorizationPolicy) handleConnect(responseWriter http.ResponseWriter, request *http.Request) {
	allowed, command, err := authorizationPolicy.permitScoped(request.Context(), "", request.Host, "443")
	if err != nil {
		http.Error(responseWriter, "sandbox network authorization failed", http.StatusBadGateway)
		return
	}
	if !allowed {
		http.Error(responseWriter, "sandbox networkController blocked host", http.StatusForbidden)
		return
	}
	requestCtx, cleanup := networkRequestContext(request.Context(), command)
	defer cleanup()
	target, err := (&net.Dialer{}).DialContext(requestCtx, "tcp", canonicalHostPort(request.Host, "443"))
	if err != nil {
		http.Error(responseWriter, err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = target.Close() }()

	hijacker, supportsHijacking := responseWriter.(http.Hijacker)
	if !supportsHijacking {
		http.Error(responseWriter, "networkController cannot hijack connection", http.StatusInternalServerError)
		return
	}
	clientConnection, hijackedReadWriter, err := hijacker.Hijack()
	if err != nil {
		http.Error(responseWriter, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = clientConnection.Close() }()
	if authorizationPolicy.owner != nil {
		if !authorizationPolicy.owner.registerTunnel(command, clientConnection, target) {
			return
		}
		defer authorizationPolicy.owner.unregisterTunnel(clientConnection, target)
	}

	if _, err := hijackedReadWriter.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := hijackedReadWriter.Flush(); err != nil {
		return
	}

	var copyWaitGroup sync.WaitGroup
	copyWaitGroup.Add(2)
	go func() {
		defer copyWaitGroup.Done()
		_, _ = io.Copy(target, hijackedReadWriter)
		closeWrite(target)
	}()
	go func() {
		defer copyWaitGroup.Done()
		_, _ = io.Copy(clientConnection, target)
		closeWrite(clientConnection)
	}()
	copyWaitGroup.Wait()
}

func isLoopbackTarget(target string) bool {
	targetHost, _, splitError := net.SplitHostPort(target)
	if splitError != nil {
		return false
	}
	targetHost = strings.Trim(targetHost, "[]")
	if strings.EqualFold(targetHost, "localhost") {
		return true
	}
	parsedIP := net.ParseIP(targetHost)
	return parsedIP != nil && parsedIP.IsLoopback()
}

func closeWrite(conn net.Conn) {
	type closeWriter interface {
		CloseWrite() error
	}
	if writableConn, supportsCloseWrite := conn.(closeWriter); supportsCloseWrite {
		_ = writableConn.CloseWrite()
	}
}

const socksHandshakeTimeout = 10 * time.Second

// socksProxyEnv names the variable that points sandboxed commands at the
// SOCKS5 endpoint.
const socksProxyEnv = "CARETZ_SOCKS_PROXY"

func (networkController *sandboxNetworkController) serveSOCKS() {
	defer close(networkController.socksDone)
	for {
		client, err := networkController.socksListener.Accept()
		if err != nil {
			return
		}
		networkController.authorizationPolicy.mu.RLock()
		command := networkController.authorizationPolicy.command
		networkController.authorizationPolicy.mu.RUnlock()
		if !networkController.registerTunnel(command, client) {
			continue
		}
		go func() {
			defer networkController.unregisterTunnel(client)
			defer func() { _ = client.Close() }()
			networkController.handleSOCKS(client, command)
		}()
	}
}

func (networkController *sandboxNetworkController) handleSOCKS(client net.Conn, acceptedCommand *commandNetworkPolicy) {
	_ = client.SetDeadline(time.Now().Add(socksHandshakeTimeout))
	header := make([]byte, 2)
	if _, err := io.ReadFull(client, header); err != nil || header[0] != 5 || header[1] == 0 {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(client, methods); err != nil {
		return
	}
	noAuth := false
	for _, method := range methods {
		noAuth = noAuth || method == 0
	}
	if !noAuth {
		_, _ = client.Write([]byte{5, 0xff})
		return
	}
	if _, err := client.Write([]byte{5, 0}); err != nil {
		return
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(client, request); err != nil || request[0] != 5 || request[2] != 0 {
		return
	}
	if request[1] != 1 {
		writeSOCKSReply(client, 7)
		return
	}
	host, err := readSOCKSHost(client, request[3])
	if err != nil {
		writeSOCKSReply(client, 8)
		return
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(client, portBytes); err != nil {
		return
	}
	port := strconv.Itoa(int(portBytes[0])<<8 | int(portBytes[1]))
	if port == "0" {
		writeSOCKSReply(client, 1)
		return
	}
	authCtx, stopWatching := watchSOCKSClient(client)
	allowed, command, err := networkController.authorizationPolicy.permitScoped(authCtx, "tcp", net.JoinHostPort(host, port), "")
	early := stopWatching()
	if err != nil || !allowed {
		writeSOCKSReply(client, 2)
		return
	}
	if command != acceptedCommand && command != nil {
		writeSOCKSReply(client, 1)
		return
	}
	if !networkController.rescopeTunnel(client, command) {
		return
	}
	requestCtx, cleanup := networkRequestContext(context.Background(), command)
	defer cleanup()
	target, err := (&net.Dialer{}).DialContext(requestCtx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		writeSOCKSReply(client, 5)
		return
	}
	defer func() { _ = target.Close() }()
	if !networkController.registerTunnel(command, target) {
		return
	}
	defer networkController.unregisterTunnel(target)
	_ = client.SetDeadline(time.Time{})
	if !writeSOCKSReply(client, 0) {
		return
	}
	if len(early) > 0 {
		if _, err := target.Write(early); err != nil {
			return
		}
	}
	relayConnections(client, target)
}

// watchSOCKSClient cancels the returned context if the client disconnects
// while its request waits for authorization, so an abandoned request does not
// leave an approval prompt open. stop ends the watch and returns any bytes the
// client sent early so they can be forwarded once the tunnel opens.
func watchSOCKSClient(client net.Conn) (ctx context.Context, stop func() []byte) {
	ctx, cancel := context.WithCancel(context.Background())
	stopping := make(chan struct{})
	done := make(chan struct{})
	var early []byte
	_ = client.SetReadDeadline(time.Time{})
	go func() {
		defer close(done)
		buf := make([]byte, 1)
		n, err := client.Read(buf)
		early = buf[:n]
		select {
		case <-stopping:
		default:
			if err != nil {
				cancel()
			}
		}
	}()
	return ctx, func() []byte {
		close(stopping)
		_ = client.SetReadDeadline(time.Now())
		<-done
		cancel()
		_ = client.SetDeadline(time.Now().Add(socksHandshakeTimeout))
		return early
	}
}

func readSOCKSHost(conn net.Conn, addressType byte) (string, error) {
	var size int
	switch addressType {
	case 1:
		size = net.IPv4len
	case 4:
		size = net.IPv6len
	case 3:
		length := []byte{0}
		if _, err := io.ReadFull(conn, length); err != nil || length[0] == 0 {
			return "", errors.New("invalid SOCKS domain")
		}
		size = int(length[0])
	default:
		return "", errors.New("unsupported SOCKS address type")
	}
	value := make([]byte, size)
	if _, err := io.ReadFull(conn, value); err != nil {
		return "", err
	}
	if addressType == 3 {
		return string(value), nil
	}
	return net.IP(value).String(), nil
}

func writeSOCKSReply(conn net.Conn, status byte) bool {
	_, err := conn.Write([]byte{5, status, 0, 1, 0, 0, 0, 0, 0, 0})
	return err == nil
}

func relayConnections(left, right net.Conn) {
	var wait sync.WaitGroup
	wait.Add(2)
	go func() { defer wait.Done(); _, _ = io.Copy(right, left); closeWrite(right) }()
	go func() { defer wait.Done(); _, _ = io.Copy(left, right); closeWrite(left) }()
	wait.Wait()
}

func parseNetworkRules(values []string) ([]networkRule, error) {
	rules := make([]networkRule, 0, len(values))
	for _, value := range values {
		rule, err := parseNetworkRule(value)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func parseNetworkRule(value string) (networkRule, error) {
	ruleText := strings.TrimSpace(value)
	if ruleText == "" {
		return networkRule{}, fmt.Errorf("network allowlist entry must not be empty")
	}
	protocol := ""
	if strings.HasPrefix(strings.ToLower(ruleText), "tcp://") {
		protocol = "tcp"
		ruleText = ruleText[len("tcp://"):]
	} else if strings.Contains(ruleText, "://") || strings.ContainsAny(ruleText, "/?#@") {
		return networkRule{}, fmt.Errorf("network allowlist entry %q must be HOST, HOST:PORT, *:PORT, tcp://HOST:PORT, or tcp://*:PORT", value)
	}

	host := ruleText
	port := ""
	if splitHost, splitPort, splitError := net.SplitHostPort(ruleText); splitError == nil {
		host = splitHost
		port = splitPort
	} else if strings.Count(ruleText, ":") == 1 {
		shortHost, shortPort, foundPort := strings.Cut(ruleText, ":")
		if foundPort && shortPort != "" {
			host = shortHost
			port = shortPort
		}
	}
	if port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return networkRule{}, fmt.Errorf("network allowlist entry %q has invalid port", value)
		}
	}
	if protocol == "tcp" && port == "" {
		return networkRule{}, fmt.Errorf("network allowlist entry %q must specify a port for tcp://", value)
	}

	host = normalizeRuleHost(host)
	if host == "" {
		return networkRule{}, fmt.Errorf("network allowlist entry %q has empty host", value)
	}
	if host == "*" {
		if port == "" {
			return networkRule{}, fmt.Errorf("network allowlist entry %q must specify a port when allowing any host", value)
		}
		return networkRule{protocol: protocol, anyHost: true, port: port}, nil
	}
	wildcard := false
	if strings.HasPrefix(host, "*.") {
		wildcard = true
		host = strings.TrimPrefix(host, "*.")
		if host == "" || strings.Contains(host, "*") {
			return networkRule{}, fmt.Errorf("network allowlist entry %q has invalid wildcard host", value)
		}
	} else if strings.Contains(host, "*") {
		return networkRule{}, fmt.Errorf("network allowlist entry %q has invalid wildcard host", value)
	}
	return networkRule{protocol: protocol, host: host, wildcard: wildcard, port: port}, nil
}

func hostAllowed(rules []networkRule, protocol, requestHost, defaultPort string) bool {
	host, port := splitRequestHost(requestHost, defaultPort)
	if host == "" {
		return false
	}
	for _, rule := range rules {
		if rule.protocol != protocol {
			continue
		}
		if rule.port != "" && rule.port != port {
			continue
		}
		if rule.anyHost {
			return true
		}
		if rule.wildcard {
			if strings.HasSuffix(host, "."+rule.host) && host != rule.host {
				return true
			}
			continue
		}
		if host == rule.host {
			return true
		}
	}
	return false
}

func splitRequestHost(value, fallbackPort string) (string, string) {
	host := value
	port := fallbackPort
	if splitHost, splitPort, splitError := net.SplitHostPort(value); splitError == nil {
		host = splitHost
		port = splitPort
	} else if strings.Count(value, ":") == 1 {
		if shortHost, shortPort, foundPort := strings.Cut(value, ":"); foundPort && shortPort != "" {
			host = shortHost
			port = shortPort
		}
	}
	return normalizeRuleHost(host), port
}

func canonicalHostPort(value, fallbackPort string) string {
	host, port := splitRequestHost(value, fallbackPort)
	return net.JoinHostPort(host, port)
}

func normalizeRuleHost(host string) string {
	host = strings.TrimSpace(host)
	host = strings.Trim(host, "[]")
	host = strings.TrimSuffix(host, ".")
	return strings.ToLower(host)
}

func defaultPort(scheme string) string {
	if scheme == "http" {
		return "80"
	}
	return "443"
}

func cloneURL(sourceURL *url.URL) *url.URL {
	copied := *sourceURL
	return &copied
}

func copyHeader(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func proxyEnv(httpAddr, socksAddr string, bypassLocalhost bool) []string {
	proxyURL := "http://" + httpAddr
	result := []string{
		"HTTP_PROXY=" + proxyURL,
		"HTTPS_PROXY=" + proxyURL,
		"ALL_PROXY=" + proxyURL,
		"http_proxy=" + proxyURL,
		"https_proxy=" + proxyURL,
		"all_proxy=" + proxyURL,
	}
	// SOCKS stays out of ALL_PROXY: HTTP clients such as httpx read ALL_PROXY
	// and fail without SOCKS support, and SOCKS accepts only tcp:// rules.
	if socksAddr != "" {
		result = append(result, socksProxyEnv+"=socks5h://"+socksAddr)
	}
	if bypassLocalhost {
		result = append(result, "NO_PROXY=localhost,127.0.0.1,::1", "no_proxy=localhost,127.0.0.1,::1")
	}
	return result
}

func directProxyTransport() http.RoundTripper {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return transport
}
