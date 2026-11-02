package chatgptauth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/lib/llm"
)

// fakeOAuth is an OpenAI-like OAuth server: discovery, JWKS, token and
// revocation endpoints, with ID tokens signed by a test key.
type fakeOAuth struct {
	t      *testing.T
	server *httptest.Server
	key    *rsa.PrivateKey

	mu            sync.Mutex
	challenge     string // code_challenge of the last authorization
	nonce         string
	refreshCalls  atomic.Int32
	refreshError  string
	revoked       []string
	issuedRefresh int
}

func newFakeOAuth(t *testing.T) *fakeOAuth {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOAuth{t: t, key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/discovery", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"jwks_uri":            f.server.URL + "/jwks",
			"revocation_endpoint": f.server.URL + "/revoke",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", f.token)
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.revoked = append(f.revoked, r.Form.Get("token"))
		f.mu.Unlock()
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeOAuth) client() *Client {
	return &Client{
		HTTPClient: f.server.Client(),
		Endpoints: Endpoints{
			Authorize: f.server.URL + "/authorize",
			Token:     f.server.URL + "/token",
			Discovery: f.server.URL + "/discovery",
		},
		Issuer: Issuer,
		Now:    time.Now,
	}
}

func (f *fakeOAuth) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	if r.Form.Get("resource") != Resource {
		f.t.Errorf("token request resource = %q", r.Form.Get("resource"))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":"invalid_grant","error_description":"PKCE mismatch"}`)
			return
		}
		if r.Form.Get("client_id") != "oaiapp_test" || r.Form.Get("code") != "the-code" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":"invalid_grant"}`)
			return
		}
		f.writeTokens(w, f.idToken("oaiapp_test", f.nonce))
	case "refresh_token":
		f.refreshCalls.Add(1)
		if r.Form.Has("scope") {
			f.t.Errorf("refresh must omit scope to keep the grant")
		}
		if f.refreshError != "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"error":%q}`, f.refreshError)
			return
		}
		// Rotation: only the latest refresh token works.
		if want := fmt.Sprintf("refresh-%d", f.issuedRefresh); r.Form.Get("refresh_token") != want {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":"refresh_token_reused"}`)
			return
		}
		f.writeTokens(w, "")
	}
}

func (f *fakeOAuth) writeTokens(w http.ResponseWriter, idToken string) {
	f.issuedRefresh++
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  fmt.Sprintf("access-%d", f.issuedRefresh),
		"refresh_token": fmt.Sprintf("refresh-%d", f.issuedRefresh),
		"id_token":      idToken,
		"expires_in":    3600,
		"scope":         scopes,
	})
}

func (f *fakeOAuth) idToken(audience, nonce string) string {
	return signJWT(f.t, f.key, map[string]any{
		"iss": Issuer, "sub": "user-1", "aud": []string{audience},
		"exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce, "email": "dev@example.com",
	})
}

func signJWT(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"k1","typ":"JWT"}`))
	payload, _ := json.Marshal(claims)
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestLoginRunsPKCEFlowAndVerifiesIDToken(t *testing.T) {
	fake := newFakeOAuth(t)
	var registered config.OAuthCredential
	cred, err := fake.client().Login(context.Background(), LoginOptions{
		ListenAddr: "127.0.0.1:0",
		Out:        io.Discard,
		OnRegistered: func(c config.OAuthCredential) error {
			registered = c
			return nil
		},
		OpenBrowser: func(authURL string) error {
			u, err := url.Parse(authURL)
			if err != nil {
				return err
			}
			q := u.Query()
			for key, want := range map[string]string{
				"client_id": dynamicClientID, "agent_name_hint": agentName, "response_type": "code",
				"code_challenge_method": "S256", "scope": scopes, "resource": Resource,
			} {
				if got := q.Get(key); got != want {
					t.Errorf("authorize %s = %q, want %q", key, got, want)
				}
			}
			if !strings.HasPrefix(q.Get("ext_agent_host_id"), "urn:uuid:") {
				t.Errorf("ext_agent_host_id = %q", q.Get("ext_agent_host_id"))
			}
			redirect, err := url.Parse(q.Get("redirect_uri"))
			if err != nil || redirect.Hostname() != "127.0.0.1" || redirect.Path != callbackPath {
				t.Errorf("redirect_uri = %q, want a 127.0.0.1 loopback callback", q.Get("redirect_uri"))
			}
			fake.mu.Lock()
			fake.challenge, fake.nonce = q.Get("code_challenge"), q.Get("nonce")
			fake.mu.Unlock()
			// The browser comes back to the loopback listener.
			go func() {
				callback := redirect.String() + "?" + url.Values{
					"code": {"the-code"}, "state": {q.Get("state")}, "client_id": {"oaiapp_test"},
				}.Encode()
				if resp, err := http.Get(callback); err == nil {
					_ = resp.Body.Close()
				}
			}()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Login() error: %v", err)
	}
	if registered.ClientID != "oaiapp_test" || registered.AccessToken != "" {
		t.Fatalf("OnRegistered got %+v, want the issued client ID before any token", registered)
	}
	if cred.ClientID != "oaiapp_test" || cred.RefreshToken != "refresh-1" || cred.Email != "dev@example.com" || cred.Subject != "user-1" {
		t.Fatalf("credential = %+v", cred)
	}
	if !cred.HasScope(PlanScope) {
		t.Fatalf("scope %q lacks %s", cred.Scope, PlanScope)
	}
	if cred.HostID != registered.HostID {
		t.Fatalf("host ID changed during login")
	}
}

func TestLoginReusesRegistration(t *testing.T) {
	c := NewClient()
	previous := config.OAuthCredential{ClientID: "oaiapp_old", HostID: "urn:uuid:fixed", Email: "dev@example.com"}
	q := mustQuery(t, c.authorizeURL(previous, "http://127.0.0.1:1455/auth/callback", "v", "s", "n"))
	if q.Get("client_id") != "oaiapp_old" || q.Get("ext_agent_host_id") != "urn:uuid:fixed" {
		t.Fatalf("reauthorization must reuse the registration: %v", q)
	}
	if q.Has("agent_name_hint") {
		t.Fatal("agent_name_hint is only for the first registration")
	}
	if q.Get("login_hint") != "dev@example.com" {
		t.Fatalf("login_hint = %q", q.Get("login_hint"))
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}

func TestVerifyIDTokenRejectsBadTokens(t *testing.T) {
	fake := newFakeOAuth(t)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	valid := map[string]any{
		"iss": Issuer, "sub": "user-1", "aud": "oaiapp_test",
		"exp": time.Now().Add(time.Hour).Unix(), "nonce": "n1",
	}
	with := func(key string, value any) map[string]any {
		claims := map[string]any{}
		for k, v := range valid {
			claims[k] = v
		}
		claims[key] = value
		return claims
	}
	cases := map[string]string{
		"wrong signer":   signJWT(t, other, valid),
		"wrong issuer":   signJWT(t, fake.key, with("iss", "https://evil.example")),
		"wrong audience": signJWT(t, fake.key, with("aud", "someone-else")),
		"expired":        signJWT(t, fake.key, with("exp", time.Now().Add(-time.Minute).Unix())),
		"wrong nonce":    signJWT(t, fake.key, with("nonce", "replayed")),
	}
	for name, token := range cases {
		if _, err := fake.client().verifyIDToken(context.Background(), token, "oaiapp_test", "n1"); err == nil {
			t.Errorf("%s: verifyIDToken accepted the token", name)
		}
	}
	if _, err := fake.client().verifyIDToken(context.Background(), signJWT(t, fake.key, valid), "oaiapp_test", "n1"); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
}

func storeSignIn(t *testing.T, path string, cred config.OAuthCredential) {
	t.Helper()
	if err := config.SaveProviderOAuth(path, "chatgpt", cred); err != nil {
		t.Fatal(err)
	}
}

func expiredSignIn() config.OAuthCredential {
	return config.OAuthCredential{
		ClientID: "oaiapp_test", HostID: "urn:uuid:x",
		AccessToken: "access-0", RefreshToken: "refresh-0",
		ExpiresAt: time.Now().Add(-time.Minute), Scope: scopes,
	}
}

func TestTokenSourceRefreshesAndPersistsRotation(t *testing.T) {
	fake := newFakeOAuth(t)
	path := filepath.Join(t.TempDir(), "credentials.json")
	storeSignIn(t, path, expiredSignIn())

	source := NewTokenSource(fake.client(), path, "chatgpt")
	token, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() error: %v", err)
	}
	if token != "access-1" {
		t.Fatalf("token = %q, want the refreshed access-1", token)
	}
	if _, err := source.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fake.refreshCalls.Load(); got != 1 {
		t.Fatalf("refreshes = %d, want 1: a fresh token is reused", got)
	}
	creds, err := config.LoadCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored := creds.ProviderOAuth("chatgpt"); stored.RefreshToken != "refresh-1" || stored.ClientID != "oaiapp_test" {
		t.Fatalf("stored = %+v, want the rotated refresh token and the same client", stored)
	}
}

func TestTokenSourcesInSeparateProcessesRefreshOnce(t *testing.T) {
	fake := newFakeOAuth(t)
	path := filepath.Join(t.TempDir(), "credentials.json")
	storeSignIn(t, path, expiredSignIn())

	// Separate TokenSources share nothing in memory, like two caretz
	// processes; only the file lock keeps them from spending the same
	// rotating refresh token twice.
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := NewTokenSource(fake.client(), path, "chatgpt").Token(context.Background()); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("Token() error: %v", err)
	}
	if got := fake.refreshCalls.Load(); got != 1 {
		t.Fatalf("refreshes = %d, want exactly 1", got)
	}
}

func TestTokenSourceClearsDeadSignIn(t *testing.T) {
	fake := newFakeOAuth(t)
	fake.refreshError = "invalid_grant"
	path := filepath.Join(t.TempDir(), "credentials.json")
	storeSignIn(t, path, expiredSignIn())

	_, err := NewTokenSource(fake.client(), path, "chatgpt").Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "caretz auth login chatgpt") {
		t.Fatalf("error = %v, want a prompt to sign in again", err)
	}
	creds, _ := config.LoadCredentials(path)
	stored := creds.ProviderOAuth("chatgpt")
	if stored.SignedIn() || stored.AccessToken != "" {
		t.Fatalf("dead tokens kept: %+v", stored)
	}
	if stored.ClientID != "oaiapp_test" || stored.HostID == "" {
		t.Fatalf("registration lost: %+v", stored)
	}
}

func TestTokenSourceKeepsSignInOnTransientFailure(t *testing.T) {
	fake := newFakeOAuth(t)
	fake.refreshError = "server_error"
	path := filepath.Join(t.TempDir(), "credentials.json")
	storeSignIn(t, path, expiredSignIn())

	if _, err := NewTokenSource(fake.client(), path, "chatgpt").Token(context.Background()); err == nil {
		t.Fatal("Token() succeeded, want the refresh error")
	}
	creds, _ := config.LoadCredentials(path)
	if !creds.ProviderOAuth("chatgpt").SignedIn() {
		t.Fatal("a temporary failure must not erase the sign-in")
	}
}

func TestTokenSourceRequiresPlanScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	cred := expiredSignIn()
	cred.Scope = "openid email offline_access"
	storeSignIn(t, path, cred)

	_, err := NewTokenSource(newFakeOAuth(t).client(), path, "chatgpt").Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "plan usage") {
		t.Fatalf("error = %v, want plan usage not granted", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportRefreshesOnceAfterUnauthorized(t *testing.T) {
	fake := newFakeOAuth(t)
	path := filepath.Join(t.TempDir(), "credentials.json")
	cred := expiredSignIn()
	cred.ExpiresAt = time.Now().Add(time.Hour) // looks fresh, but the API revoked it
	storeSignIn(t, path, cred)

	var seen []string
	transport := &Transport{
		Source: NewTokenSource(fake.client(), path, "chatgpt"),
		Base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(r.Body)
			seen = append(seen, r.Header.Get("Authorization")+" "+string(body))
			status := http.StatusOK
			if len(seen) == 1 {
				status = http.StatusUnauthorized
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
		}),
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader([]byte(`{"x":1}`)))
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the retried 200", resp.StatusCode)
	}
	want := []string{`Bearer access-0 {"x":1}`, `Bearer access-1 {"x":1}`}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("requests = %q, want %q", seen, want)
	}
}

func TestTransportRefusesOtherHosts(t *testing.T) {
	transport := &Transport{
		Source: NewTokenSource(&Client{Now: time.Now}, filepath.Join(t.TempDir(), "c.json"), "chatgpt"),
		Base: roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("request reached the network")
			return nil, nil
		}),
	}
	for _, target := range []string{"https://evil.example/v1/responses", "http://api.openai.com/v1/responses"} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		if _, err := transport.RoundTrip(req); err == nil {
			t.Errorf("%s: transport sent ChatGPT credentials", target)
		}
	}
}

func TestProviderOptionsLeavesAPIKeysAlone(t *testing.T) {
	key, opts, err := ProviderOptions("sk-plain", llm.OpenAIProviderOptions{})
	if err != nil || key != "sk-plain" || opts.ChatGPTPlan || opts.HTTPClient != nil {
		t.Fatalf("ProviderOptions changed a plain key: %q %+v %v", key, opts, err)
	}
	t.Setenv(config.CredentialsFileEnv, filepath.Join(t.TempDir(), "credentials.json"))
	key, opts, err = ProviderOptions(config.OAuthKeyRef("chatgpt"), llm.OpenAIProviderOptions{})
	if err != nil || key != "" || !opts.ChatGPTPlan || opts.HTTPClient == nil {
		t.Fatalf("ProviderOptions(ref) = %q %+v %v", key, opts, err)
	}
	if _, ok := opts.HTTPClient.Transport.(*Transport); !ok {
		t.Fatalf("transport = %T, want *Transport", opts.HTTPClient.Transport)
	}
}
