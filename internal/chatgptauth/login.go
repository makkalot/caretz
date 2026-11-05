package chatgptauth

import (
	"bufio"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/makkalot/caretz/internal/config"
)

const (
	callbackPath = "/auth/callback"
	// callbackAddr is the loopback address OpenAI expects. The host must be
	// 127.0.0.1, never localhost; only the port may change, so a busy port
	// falls back to any free one.
	callbackAddr = "127.0.0.1:1455"
)

// LoginOptions configures one browser sign-in.
type LoginOptions struct {
	// Previous is the provider's stored sign-in, if any. Its ClientID and
	// HostID are reused so signing in again keeps the same registration.
	Previous *config.OAuthCredential
	// Out receives the sign-in URL, printed for when no browser opens.
	Out io.Writer
	// OpenBrowser opens the URL; nil uses the system browser.
	OpenBrowser func(string) error
	// OnRegistered is called with the issued client ID before the code is
	// exchanged, so a failed exchange still keeps the registration.
	OnRegistered func(config.OAuthCredential) error
	// ListenAddr overrides the callback listener, for tests.
	ListenAddr string
	// Paste, when set, is where the person pastes the URL the browser was
	// redirected to, for when that browser can't reach the callback listener.
	// No browser is opened. Login returns only after any read it started has
	// finished, so the caller can go on reading from the same stream. A paste
	// reader that never returns blocks Login, so pass only a terminal.
	Paste io.Reader
}

// Login runs the browser authorization code flow with PKCE and returns the
// new sign-in. It waits until the browser returns or ctx ends.
func (c *Client) Login(ctx context.Context, opts LoginOptions) (config.OAuthCredential, error) {
	cred := config.OAuthCredential{}
	if opts.Previous != nil {
		cred.ClientID, cred.HostID = opts.Previous.ClientID, opts.Previous.HostID
	}
	if cred.HostID == "" {
		cred.HostID = newHostID()
	}
	verifier := randomString(32)
	state := randomString(16)
	nonce := randomString(16)

	listener, err := listen(cmp.Or(opts.ListenAddr, callbackAddr))
	if err != nil {
		return config.OAuthCredential{}, err
	}
	redirectURI := fmt.Sprintf("http://%s%s", listener.Addr().String(), callbackPath)

	results := make(chan callbackResult, 1)
	server := &http.Server{Handler: callbackHandler(state, results), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()

	authURL := c.authorizeURL(cred, redirectURI, verifier, state, nonce)
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	if opts.Paste != nil {
		_, _ = fmt.Fprintf(out, "Open this URL in a browser on any machine and sign in with ChatGPT:\n\n  %s\n\nThe browser then fails to load a 127.0.0.1 page. That is expected: copy the\nfull URL from its address bar and paste it here.\n\nPasted URL: ", authURL)
	} else {
		_, _ = fmt.Fprintf(out, "Opening your browser to sign in with ChatGPT. If it does not open, visit:\n\n  %s\n\n", authURL)
		openBrowser := opts.OpenBrowser
		if openBrowser == nil {
			openBrowser = systemBrowser
		}
		_ = openBrowser(authURL)
	}

	result, err := awaitCallback(ctx, results, opts.Paste, out, state)
	if err != nil {
		return config.OAuthCredential{}, err
	}
	if result.err != nil {
		return config.OAuthCredential{}, result.err
	}
	if result.clientID != "" {
		cred.ClientID = result.clientID
	}
	if cred.ClientID == "" {
		return config.OAuthCredential{}, errors.New("ChatGPT sign-in: OpenAI did not return a client ID")
	}
	if opts.OnRegistered != nil {
		if err := opts.OnRegistered(cred); err != nil {
			return config.OAuthCredential{}, err
		}
	}
	tok, err := c.exchange(ctx, cred.ClientID, result.code, verifier, redirectURI)
	if err != nil {
		return cred, err
	}
	if tok.IDToken == "" {
		return cred, errors.New("ChatGPT sign-in: token response has no ID token")
	}
	claims, err := c.verifyIDToken(ctx, tok.IDToken, cred.ClientID, nonce)
	if err != nil {
		return cred, err
	}
	if tok.RefreshToken == "" {
		return cred, errors.New("ChatGPT sign-in: token response has no refresh token")
	}
	c.apply(&cred, tok)
	cred.Subject, cred.Email = claims.Subject, claims.Email
	return cred, nil
}

func (c *Client) authorizeURL(cred config.OAuthCredential, redirectURI, verifier, state, nonce string) string {
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {scopes},
		"resource":              {Resource},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"ext_agent_host_id":     {cred.HostID},
	}
	if cred.ClientID != "" {
		q.Set("client_id", cred.ClientID)
		if cred.Email != "" {
			q.Set("login_hint", cred.Email)
		}
	} else {
		q.Set("client_id", dynamicClientID)
		q.Set("agent_name_hint", agentName)
	}
	return c.Endpoints.Authorize + "?" + q.Encode()
}

type callbackResult struct {
	code, clientID string
	err            error
}

// awaitCallback waits for the browser's callback or, when paste is set, for
// the person to paste the URL it was redirected to. A stream that ends just
// leaves the browser callback to wait for.
func awaitCallback(ctx context.Context, results <-chan callbackResult, paste io.Reader, out io.Writer, state string) (callbackResult, error) {
	var lines *bufio.Reader
	var line <-chan pastedLine
	if paste != nil {
		lines = bufio.NewReader(paste)
		line = readPastedLine(lines)
	}
	// A read still in flight would swallow the caller's next input, or race
	// with it, and terminal reads can't be cancelled, so let the person end it.
	finishRead := func() {
		if line != nil {
			_, _ = fmt.Fprint(out, "\nPress Enter to continue.\n")
			<-line
		}
	}
	for {
		select {
		case result := <-results:
			finishRead()
			return result, nil
		case <-ctx.Done():
			finishRead()
			return callbackResult{}, fmt.Errorf("ChatGPT sign-in: %w", ctx.Err())
		case pasted := <-line:
			text := strings.TrimSpace(pasted.text)
			if pasted.err != nil && text == "" {
				line = nil
				continue
			}
			if text != "" {
				result, problem := parsePastedCallback(text, state)
				if problem == "" {
					return result, nil
				}
				_, _ = fmt.Fprintf(out, "%s\n\nPasted URL: ", problem)
			}
			line = readPastedLine(lines)
		}
	}
}

type pastedLine struct {
	text string
	err  error
}

func readPastedLine(lines *bufio.Reader) <-chan pastedLine {
	ch := make(chan pastedLine, 1)
	go func() {
		text, err := lines.ReadString('\n')
		ch <- pastedLine{text, err}
	}()
	return ch
}

// parsePastedCallback reads a pasted redirect URL, or returns what to tell the
// person instead. The message never repeats the URL, which holds the code.
func parsePastedCallback(text, state string) (callbackResult, string) {
	u, err := url.Parse(text)
	if err != nil || u.Path != callbackPath {
		return callbackResult{}, "That is not the sign-in redirect. Copy the full URL from the address bar; its path is " + callbackPath + "."
	}
	result, stale := parseCallback(u.Query(), state)
	if stale {
		return callbackResult{}, "That URL is from a different sign-in. Use the URL printed above, then paste where it redirects."
	}
	return result, ""
}

// parseCallback reads the callback's query. stale means it doesn't carry the
// expected state, so it must be ignored.
func parseCallback(q url.Values, state string) (result callbackResult, stale bool) {
	switch {
	case q.Get("error") != "":
		result.err = fmt.Errorf("ChatGPT sign-in was not completed: %s", strings.TrimSpace(q.Get("error")+" "+q.Get("error_description")))
	case q.Get("state") != state:
		return callbackResult{}, true
	case q.Get("code") == "":
		result.err = errors.New("ChatGPT sign-in: callback has no code")
	default:
		result.code, result.clientID = q.Get("code"), q.Get("client_id")
	}
	return result, false
}

// Headless reports whether a browser opened here can't be the one the person
// uses: an SSH session, or Linux with no display.
func Headless() bool {
	return headless(runtime.GOOS, os.Getenv)
}

func headless(goos string, getenv func(string) string) bool {
	if getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" {
		return true
	}
	return goos == "linux" && getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == ""
}

// callbackHandler accepts the first callback that carries the expected
// state; later requests only get a page.
func callbackHandler(state string, results chan<- callbackResult) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != callbackPath {
			http.NotFound(w, r)
			return
		}
		result, stale := parseCallback(r.URL.Query(), state)
		if stale {
			http.Error(w, "This sign-in link is stale. Start again from caretz.", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		message := "Signed in. You can close this tab and return to caretz."
		if result.err != nil {
			message = result.err.Error()
		}
		_, _ = fmt.Fprintf(w, "<!doctype html><title>caretz</title><p>%s</p>\n", html.EscapeString(message))
		select {
		case results <- result:
		default:
		}
	})
}

func listen(addr string) (net.Listener, error) {
	listener, err := net.Listen("tcp", addr)
	if err == nil {
		return listener, nil
	}
	host, _, _ := net.SplitHostPort(addr)
	listener, fallbackErr := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if fallbackErr != nil {
		return nil, fmt.Errorf("ChatGPT sign-in: listen for the browser callback: %w", err)
	}
	return listener, nil
}

func systemBrowser(target string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, target)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// newHostID returns a random per-installation ID as a urn:uuid (version 4).
func newHostID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
