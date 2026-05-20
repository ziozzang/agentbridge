// Package xaioauth resolves xAI Grok OAuth bearer tokens.
//
// It intentionally mirrors the Hermes Agent xai-oauth flow enough for
// AgentBridge runtime use:
//   - read a flat AgentBridge token file, or
//   - read ~/.grok/auth.json providers["xai-oauth"] entry, or
//   - read Hermes' ~/.hermes/auth.json providers["xai-oauth"] entry as a fallback, and
//   - refresh expiring JWT access tokens through xAI's OIDC token endpoint.
//
// The interactive browser PKCE login is not implemented here yet; use Hermes
// (`hermes auth add xai-oauth`) or set AGENTBRIDGE_XAI_OAUTH_ACCESS_TOKEN for
// bootstrap.
package xaioauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	DefaultIssuer       = "https://auth.x.ai"
	DefaultDiscoveryURL = DefaultIssuer + "/.well-known/openid-configuration"
	DefaultClientID     = "b1a00492-073a-47ea-816f-4c329264a828"
	DefaultProviderID   = "xai-oauth"
	refreshSkew         = 120 * time.Second
)

// Token is the cached xAI OAuth token.
type Token struct {
	AccessToken   string    `json:"access_token"`
	RefreshToken  string    `json:"refresh_token"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
	ClientID      string    `json:"client_id,omitempty"`
	TokenURL      string    `json:"token_url,omitempty"`
	TokenEndpoint string    `json:"token_endpoint,omitempty"`
}

// Resolver reads and refreshes xAI OAuth tokens.
type Resolver struct {
	path       string
	httpClient *http.Client
	mu         sync.Mutex
	tok        *Token
}

// New constructs a Resolver. If tokenPath is empty it tries ~/.grok/auth.json
// first, then Hermes' auth store as a migration fallback.
func New(tokenPath string) *Resolver {
	if tokenPath == "" {
		tokenPath = defaultTokenPath()
	}
	return &Resolver{path: tokenPath, httpClient: http.DefaultClient}
}

// Resolve returns a valid access token.
func (r *Resolver) Resolve(ctx context.Context) (string, error) {
	tok, err := r.ResolveToken(ctx)
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// ResolveToken returns a valid access token, refreshing if needed.
func (r *Resolver) ResolveToken(ctx context.Context) (*Token, error) {
	if v := envFirst("AGENTBRIDGE_XAI_OAUTH_ACCESS_TOKEN", "XAI_OAUTH_ACCESS_TOKEN"); v != "" {
		return &Token{AccessToken: v}, nil
	}
	tok, err := r.loadCached()
	if err != nil {
		return nil, err
	}
	if !tok.expiringSoon() {
		return tok, nil
	}
	if tok.RefreshToken == "" {
		if v := envFirst("AGENTBRIDGE_XAI_OAUTH_REFRESH_TOKEN", "XAI_OAUTH_REFRESH_TOKEN"); v != "" {
			tok.RefreshToken = v
		} else {
			return nil, fmt.Errorf("xai oauth: access token expired and no refresh token available")
		}
	}
	refreshed, err := r.refresh(ctx, tok)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.tok = refreshed
	r.mu.Unlock()
	_ = r.persist(refreshed)
	return refreshed, nil
}

func (r *Resolver) loadCached() (*Token, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tok != nil {
		return r.tok, nil
	}
	if r.path == "" || !fileExists(r.path) {
		if rt := envFirst("AGENTBRIDGE_XAI_OAUTH_REFRESH_TOKEN", "XAI_OAUTH_REFRESH_TOKEN"); rt != "" {
			r.tok = &Token{RefreshToken: rt}
			return r.tok, nil
		}
		return nil, fmt.Errorf("xai oauth: token file not found at %s and no AGENTBRIDGE_XAI_OAUTH_REFRESH_TOKEN set", r.path)
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		return nil, err
	}
	t, err := parseToken(data)
	if err != nil {
		return nil, fmt.Errorf("xai oauth: parse %s: %w", r.path, err)
	}
	r.tok = t
	return r.tok, nil
}

func parseToken(data []byte) (*Token, error) {
	var flat Token
	if err := json.Unmarshal(data, &flat); err != nil {
		return nil, err
	}
	if flat.AccessToken != "" || flat.RefreshToken != "" {
		return &flat, nil
	}
	var hermes struct {
		Providers map[string]struct {
			Tokens struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
				ExpiresIn    int    `json:"expires_in"`
			} `json:"tokens"`
			Discovery struct {
				TokenEndpoint string `json:"token_endpoint"`
			} `json:"discovery"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(data, &hermes); err != nil {
		return nil, err
	}
	p := hermes.Providers[DefaultProviderID]
	if p.Tokens.AccessToken == "" && p.Tokens.RefreshToken == "" {
		return nil, fmt.Errorf("missing providers.%s.tokens", DefaultProviderID)
	}
	return &Token{
		AccessToken:   p.Tokens.AccessToken,
		RefreshToken:  p.Tokens.RefreshToken,
		TokenEndpoint: p.Discovery.TokenEndpoint,
	}, nil
}

func (r *Resolver) refresh(ctx context.Context, t *Token) (*Token, error) {
	endpoint := firstNonEmpty(
		envFirst("AGENTBRIDGE_XAI_OAUTH_TOKEN_URL", "XAI_OAUTH_TOKEN_URL"),
		t.TokenEndpoint,
		t.TokenURL,
	)
	var err error
	if endpoint == "" {
		endpoint, err = r.discoverTokenEndpoint(ctx)
		if err != nil {
			return nil, err
		}
	}
	if err := validateXAIEndpoint(endpoint); err != nil {
		return nil, err
	}
	clientID := firstNonEmpty(envFirst("AGENTBRIDGE_XAI_OAUTH_CLIENT_ID", "XAI_OAUTH_CLIENT_ID"), t.ClientID, DefaultClientID)
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", clientID)
	form.Set("refresh_token", t.RefreshToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xai oauth: refresh request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("xai oauth: refresh failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("xai oauth: parse refresh response: %w", err)
	}
	if payload.AccessToken == "" {
		return nil, fmt.Errorf("xai oauth: refresh response missing access_token")
	}
	out := *t
	out.AccessToken = payload.AccessToken
	if payload.RefreshToken != "" {
		out.RefreshToken = payload.RefreshToken
	}
	out.TokenEndpoint = endpoint
	if payload.ExpiresIn > 0 {
		out.ExpiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	return &out, nil
}

func (r *Resolver) discoverTokenEndpoint(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DefaultDiscoveryURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("xai oauth: discovery request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("xai oauth: discovery failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		TokenEndpoint string `json:"token_endpoint"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	if payload.TokenEndpoint == "" {
		return "", fmt.Errorf("xai oauth: discovery missing token_endpoint")
	}
	if err := validateXAIEndpoint(payload.TokenEndpoint); err != nil {
		return "", err
	}
	return payload.TokenEndpoint, nil
}

func (r *Resolver) persist(t *Token) error {
	if r.path == "" {
		return nil
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		return err
	}
	var hermes map[string]any
	if err := json.Unmarshal(data, &hermes); err == nil {
		if providers, _ := hermes["providers"].(map[string]any); providers != nil {
			if rawState, _ := providers[DefaultProviderID].(map[string]any); rawState != nil {
				tokens, _ := rawState["tokens"].(map[string]any)
				if tokens == nil {
					tokens = map[string]any{}
				}
				tokens["access_token"] = t.AccessToken
				tokens["refresh_token"] = t.RefreshToken
				rawState["tokens"] = tokens
				rawState["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
				discovery, _ := rawState["discovery"].(map[string]any)
				if discovery == nil {
					discovery = map[string]any{}
				}
				if t.TokenEndpoint != "" {
					discovery["token_endpoint"] = t.TokenEndpoint
				}
				rawState["discovery"] = discovery
				return writeJSON0600(r.path, hermes)
			}
		}
	}
	return writeJSON0600(r.path, t)
}

func writeJSON0600(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func (t *Token) expiringSoon() bool {
	if t.AccessToken == "" {
		return true
	}
	if exp, ok := jwtExpiry(t.AccessToken); ok {
		return time.Until(exp) <= refreshSkew
	}
	if !t.ExpiresAt.IsZero() {
		return time.Until(t.ExpiresAt) <= refreshSkew
	}
	return false
}

func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	payload := parts[1]
	payload += strings.Repeat("=", (4-len(payload)%4)%4)
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(claims.Exp), 0), true
}

func validateXAIEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "https" {
		return fmt.Errorf("xai oauth: refusing non-HTTPS endpoint %q", raw)
	}
	host := strings.ToLower(u.Hostname())
	if host != "x.ai" && !strings.HasSuffix(host, ".x.ai") {
		return fmt.Errorf("xai oauth: refusing non-xAI endpoint %q", raw)
	}
	return nil
}

func defaultTokenPath() string {
	if v := envFirst("AGENTBRIDGE_XAI_OAUTH_TOKEN_FILE", "XAI_OAUTH_TOKEN_FILE"); v != "" {
		return v
	}
	if h, err := os.UserHomeDir(); err == nil {
		grok := filepath.Join(h, ".grok", "auth.json")
		if fileExists(grok) {
			return grok
		}
		hermes := filepath.Join(h, ".hermes", "auth.json")
		if fileExists(hermes) {
			return hermes
		}
		return grok
	}
	return ""
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func envFirst(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
