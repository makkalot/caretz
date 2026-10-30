package chatgptauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/lib/llm"
)

// refreshMargin refreshes an access token this long before it expires, so a
// long streamed response never starts with a token about to lapse.
const refreshMargin = 5 * time.Minute

// apiHost is the only host that ever receives a ChatGPT access token.
const apiHost = "api.openai.com"

// TokenSource hands out the access token of one provider's sign-in,
// refreshing it through credentials.json under a file lock.
type TokenSource struct {
	client          *Client
	credentialsPath string
	providerKey     string

	mu     sync.Mutex
	cached config.OAuthCredential
}

// NewTokenSource returns a TokenSource for providerKey's sign-in stored in
// credentialsPath.
func NewTokenSource(client *Client, credentialsPath, providerKey string) *TokenSource {
	return &TokenSource{client: client, credentialsPath: credentialsPath, providerKey: providerKey}
}

// Token returns a valid access token.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	return s.token(ctx, "")
}

// token returns a fresh access token. A non-empty rejected token was just
// refused by the API, so it is refreshed even though it has not expired.
func (s *TokenSource) token(ctx context.Context, rejected string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fresh(s.cached, rejected) {
		return s.cached.AccessToken, nil
	}
	cred, err := config.UpdateProviderOAuth(s.credentialsPath, s.providerKey, func(cur *config.OAuthCredential) (bool, error) {
		if !cur.SignedIn() {
			return false, s.signInError("is signed out")
		}
		if !cur.HasScope(PlanScope) {
			return false, s.signInError("was not granted ChatGPT plan usage")
		}
		// Another process may have refreshed since this one last looked.
		if s.fresh(*cur, rejected) {
			return false, nil
		}
		updated, err := s.client.Refresh(ctx, *cur)
		if err != nil {
			if IsTerminal(err) {
				cur.AccessToken, cur.RefreshToken, cur.ExpiresAt = "", "", time.Time{}
				return true, s.signInError(fmt.Sprintf("sign-in has ended (%v)", err))
			}
			return false, err
		}
		*cur = updated
		return true, nil
	})
	if errors.Is(err, config.ErrNotSignedIn) {
		return "", s.signInError("is not signed in")
	}
	if err != nil {
		return "", err
	}
	s.cached = cred
	return cred.AccessToken, nil
}

func (s *TokenSource) fresh(cred config.OAuthCredential, rejected string) bool {
	return cred.AccessToken != "" && cred.AccessToken != rejected &&
		s.client.Now().Add(refreshMargin).Before(cred.ExpiresAt)
}

func (s *TokenSource) signInError(problem string) error {
	return fmt.Errorf("ChatGPT provider %q %s; run `%s`", s.providerKey, problem, config.SignInCommand(config.AuthChatGPT, s.providerKey))
}

// Transport signs requests with a TokenSource's access token and refreshes
// it once when the API rejects it. It refuses every host but OpenAI's API, so
// the token cannot follow a misconfigured base URL anywhere else.
type Transport struct {
	Source *TokenSource
	Base   http.RoundTripper
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Hostname(), apiHost) {
		return nil, fmt.Errorf("refusing to send ChatGPT credentials to %s", req.URL.Host)
	}
	token, err := t.Source.Token(req.Context())
	if err != nil {
		return nil, err
	}
	resp, err := t.base().RoundTrip(withBearer(req, token))
	if err != nil || resp.StatusCode != http.StatusUnauthorized || (req.Body != nil && req.GetBody == nil) {
		return resp, err
	}
	retryToken, err := t.Source.token(req.Context(), token)
	if err != nil {
		return resp, nil
	}
	retry := withBearer(req, retryToken)
	if req.GetBody != nil {
		if retry.Body, err = req.GetBody(); err != nil {
			return resp, nil
		}
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	return t.base().RoundTrip(retry)
}

func (t *Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

func withBearer(req *http.Request, token string) *http.Request {
	out := req.Clone(req.Context())
	out.Header.Set("Authorization", "Bearer "+token)
	return out
}

var (
	sourcesMu sync.Mutex
	sources   = map[string]*TokenSource{}
)

// sourceFor shares one TokenSource per provider within the process, so
// concurrent subagents reuse a token instead of each refreshing it.
func sourceFor(credentialsPath, providerKey string) *TokenSource {
	sourcesMu.Lock()
	defer sourcesMu.Unlock()
	key := credentialsPath + "\x00" + providerKey
	if source, ok := sources[key]; ok {
		return source
	}
	source := NewTokenSource(NewClient(), credentialsPath, providerKey)
	sources[key] = source
	return source
}

// ProviderOptions adapts LLM client options to apiKey. When apiKey is a
// ChatGPT credential reference (config.OAuthKeyRef) it returns no key and
// options that sign requests with that provider's sign-in and shape them for
// plan usage; any other key passes through unchanged.
func ProviderOptions(apiKey string, opts llm.OpenAIProviderOptions) (string, llm.OpenAIProviderOptions, error) {
	providerKey, ok := config.ParseOAuthKeyRef(apiKey)
	if !ok {
		return apiKey, opts, nil
	}
	homePath, err := config.HomeConfigPath()
	if err != nil {
		return "", opts, err
	}
	transport := &Transport{Source: sourceFor(config.CredentialsPath(homePath), providerKey)}
	if opts.HTTPClient != nil {
		transport.Base = opts.HTTPClient.Transport
	}
	opts.HTTPClient = &http.Client{Transport: transport}
	opts.ChatGPTPlan = true
	return "", opts, nil
}

// NewProvider is llm.NewOpenAIProviderWithOptions for any configured
// provider, including one that signs in with ChatGPT.
func NewProvider(apiKey, baseURL, model string, opts llm.OpenAIProviderOptions) llm.Provider {
	key, adapted, err := ProviderOptions(apiKey, opts)
	if err != nil {
		adapted.HTTPClient = &http.Client{Transport: failingTransport{err}}
	}
	return llm.NewOpenAIProviderWithOptions(key, baseURL, model, adapted)
}

// ListModels is llm.ListOpenAIModels for any configured provider, including
// one that signs in with ChatGPT.
func ListModels(ctx context.Context, apiKey, baseURL string, opts llm.OpenAIProviderOptions) ([]string, error) {
	key, adapted, err := ProviderOptions(apiKey, opts)
	if err != nil {
		return nil, err
	}
	return llm.ListOpenAIModels(ctx, key, baseURL, adapted)
}

type failingTransport struct{ err error }

func (t failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }
