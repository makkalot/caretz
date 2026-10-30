// Package chatgptauth signs caretz in with a ChatGPT account so requests
// spend the account's plan usage instead of API credits ("Sign in with
// ChatGPT"). It runs the browser OAuth flow, keeps the rotating tokens in
// credentials.json, and supplies them to the LLM client through an HTTP
// transport that only talks to OpenAI's API host.
package chatgptauth

import (
	"cmp"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/makkalot/caretz/internal/config"
)

const (
	// Issuer is OpenAI's OAuth issuer; ID tokens must name it.
	Issuer = "https://auth.openai.com"
	// Resource is the API the access token is minted for.
	Resource = config.ChatGPTBaseURL
	// PlanScope grants inference on the account's ChatGPT plan.
	PlanScope = "chatgpt.tokens.use.direct"

	scopes          = "openid profile email offline_access resource.invoke " + PlanScope
	dynamicClientID = "dynamic_agent_client"
	agentName       = "caretz"
)

// Endpoints are the OAuth URLs; tests point them at a local server.
type Endpoints struct {
	Authorize string
	Token     string
	Discovery string
}

// DefaultEndpoints are OpenAI's production OAuth endpoints.
var DefaultEndpoints = Endpoints{
	Authorize: Issuer + "/api/accounts/authorize",
	Token:     Issuer + "/api/accounts/oauth/token",
	Discovery: Issuer + "/.well-known/openid-configuration",
}

// Client talks to the OAuth endpoints.
type Client struct {
	HTTPClient *http.Client
	Endpoints  Endpoints
	Issuer     string
	Now        func() time.Time
}

// NewClient returns a Client for OpenAI's production endpoints.
func NewClient() *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		Endpoints:  DefaultEndpoints,
		Issuer:     Issuer,
		Now:        time.Now,
	}
}

// OAuthError is an error response from the token or revocation endpoint.
type OAuthError struct {
	StatusCode  int
	Code        string
	Description string
}

func (e *OAuthError) Error() string {
	msg := cmp.Or(e.Description, e.Code, http.StatusText(e.StatusCode))
	if e.Code != "" && e.Description != "" {
		msg = e.Code + ": " + e.Description
	}
	return fmt.Sprintf("ChatGPT sign-in: %d %s", e.StatusCode, msg)
}

// terminalRefreshCodes mean the refresh token can never work again, so the
// tokens are cleared and the user signs in again.
var terminalRefreshCodes = []string{
	"invalid_grant", "invalid_refresh_token", "token_expired",
	"refresh_token_expired", "refresh_token_invalidated", "refresh_token_reused",
}

// IsTerminal reports whether err means the stored sign-in is dead and only a
// new sign-in helps; a network failure or 5xx is not.
func IsTerminal(err error) bool {
	var oauthErr *OAuthError
	return errors.As(err, &oauthErr) && slices.Contains(terminalRefreshCodes, oauthErr.Code)
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// exchange trades an authorization code for tokens.
func (c *Client) exchange(ctx context.Context, clientID, code, verifier, redirectURI string) (tokenResponse, error) {
	return c.postToken(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
		"resource":      {Resource},
	})
}

// Refresh returns cred with a new access token, and the rotated refresh
// token and granted scopes when the server sends them. Scope is omitted from
// the request so the grant stays as it was.
func (c *Client) Refresh(ctx context.Context, cred config.OAuthCredential) (config.OAuthCredential, error) {
	tok, err := c.postToken(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {cred.ClientID},
		"refresh_token": {cred.RefreshToken},
		"resource":      {Resource},
	})
	if err != nil {
		return cred, err
	}
	c.apply(&cred, tok)
	return cred, nil
}

func (c *Client) apply(cred *config.OAuthCredential, tok tokenResponse) {
	cred.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		cred.RefreshToken = tok.RefreshToken
	}
	if tok.Scope != "" {
		cred.Scope = tok.Scope
	}
	cred.ExpiresAt = c.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).UTC()
}

func (c *Client) postToken(ctx context.Context, form url.Values) (tokenResponse, error) {
	resp, err := c.postForm(ctx, c.Endpoints.Token, form)
	if err != nil {
		return tokenResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("ChatGPT sign-in: read token response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return tokenResponse{}, parseOAuthError(resp.StatusCode, body)
	}
	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return tokenResponse{}, fmt.Errorf("ChatGPT sign-in: decode token response: %w", err)
	}
	if tok.AccessToken == "" {
		return tokenResponse{}, errors.New("ChatGPT sign-in: token response has no access_token")
	}
	return tok, nil
}

// Revoke invalidates the refresh token at OpenAI. The server answers 200
// even for a token that is already invalid.
func (c *Client) Revoke(ctx context.Context, cred config.OAuthCredential) error {
	disc, err := c.discover(ctx)
	if err != nil {
		return err
	}
	if disc.RevocationEndpoint == "" {
		return errors.New("ChatGPT sign-in: discovery document has no revocation_endpoint")
	}
	resp, err := c.postForm(ctx, disc.RevocationEndpoint, url.Values{
		"token":           {cred.RefreshToken},
		"token_type_hint": {"refresh_token"},
		"client_id":       {cred.ClientID},
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return parseOAuthError(resp.StatusCode, body)
	}
	return nil
}

func (c *Client) postForm(ctx context.Context, endpoint string, form url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ChatGPT sign-in: %w", err)
	}
	return resp, nil
}

func parseOAuthError(status int, body []byte) error {
	var payload struct {
		Error            any    `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	oauthErr := &OAuthError{StatusCode: status}
	if json.Unmarshal(body, &payload) == nil {
		switch v := payload.Error.(type) {
		case string:
			oauthErr.Code = v
		case map[string]any:
			oauthErr.Code, _ = v["code"].(string)
			oauthErr.Description, _ = v["message"].(string)
		}
		oauthErr.Description = cmp.Or(payload.ErrorDescription, oauthErr.Description)
	}
	return oauthErr
}

type discovery struct {
	JWKSURI            string `json:"jwks_uri"`
	RevocationEndpoint string `json:"revocation_endpoint"`
}

func (c *Client) discover(ctx context.Context) (discovery, error) {
	var disc discovery
	err := c.getJSON(ctx, c.Endpoints.Discovery, &disc)
	return disc, err
}

func (c *Client) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("ChatGPT sign-in: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("ChatGPT sign-in: GET %s: %s", endpoint, resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return fmt.Errorf("ChatGPT sign-in: decode %s: %w", endpoint, err)
	}
	return nil
}

// idClaims are the ID token claims caretz checks or keeps.
type idClaims struct {
	Issuer   string          `json:"iss"`
	Subject  string          `json:"sub"`
	Audience json.RawMessage `json:"aud"`
	Expiry   int64           `json:"exp"`
	Nonce    string          `json:"nonce"`
	Email    string          `json:"email"`
}

// verifyIDToken checks the ID token's RS256 signature against OpenAI's
// published keys, and its issuer, audience, expiry and nonce.
func (c *Client) verifyIDToken(ctx context.Context, idToken, clientID, nonce string) (idClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return idClaims{}, errors.New("ChatGPT sign-in: malformed ID token")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return idClaims{}, err
	}
	if header.Alg != "RS256" {
		return idClaims{}, fmt.Errorf("ChatGPT sign-in: unsupported ID token algorithm %q", header.Alg)
	}
	key, err := c.signingKey(ctx, header.Kid)
	if err != nil {
		return idClaims{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return idClaims{}, errors.New("ChatGPT sign-in: malformed ID token signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return idClaims{}, errors.New("ChatGPT sign-in: ID token signature is invalid")
	}
	var claims idClaims
	if err := decodeSegment(parts[1], &claims); err != nil {
		return idClaims{}, err
	}
	switch {
	case claims.Issuer != c.Issuer:
		return idClaims{}, fmt.Errorf("ChatGPT sign-in: ID token issuer %q is not %q", claims.Issuer, c.Issuer)
	case !audienceContains(claims.Audience, clientID):
		return idClaims{}, errors.New("ChatGPT sign-in: ID token was not issued to this client")
	case c.Now().Unix() >= claims.Expiry:
		return idClaims{}, errors.New("ChatGPT sign-in: ID token has expired")
	case claims.Nonce != nonce:
		return idClaims{}, errors.New("ChatGPT sign-in: ID token nonce does not match")
	case claims.Subject == "":
		return idClaims{}, errors.New("ChatGPT sign-in: ID token has no subject")
	}
	return claims, nil
}

func (c *Client) signingKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	disc, err := c.discover(ctx)
	if err != nil {
		return nil, err
	}
	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := c.getJSON(ctx, disc.JWKSURI, &jwks); err != nil {
		return nil, err
	}
	for _, k := range jwks.Keys {
		if k.Kid != kid || k.Kty != "RSA" {
			continue
		}
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if errN != nil || errE != nil || len(e) > 4 {
			return nil, errors.New("ChatGPT sign-in: malformed signing key")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
	}
	return nil, fmt.Errorf("ChatGPT sign-in: no signing key %q", kid)
}

func audienceContains(raw json.RawMessage, clientID string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == clientID
	}
	var list []string
	return json.Unmarshal(raw, &list) == nil && slices.Contains(list, clientID)
}

func decodeSegment(segment string, out any) error {
	data, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return errors.New("ChatGPT sign-in: malformed ID token")
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errors.New("ChatGPT sign-in: malformed ID token")
	}
	return nil
}
