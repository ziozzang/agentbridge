// Package codexoauth implements the OpenAI Codex OAuth-style token
// resolver. When a provider config uses `api_key: oauth:codex`, the harness
// resolves the live access token by reading the cached token JSON (Codex
// CLI format) and, when it has expired, exchanging the refresh token at
// OpenAI's OAuth endpoint.
//
// The expected cache path is:
//
//	$ACP_HARNESS_CODEX_TOKEN_FILE        if set, or else
//	$XDG_CONFIG_HOME/acp-harness/codex-token.json or else
//	~/.config/acp-harness/codex-token.json
//
// Token file shape:
//
//	{
//	  "access_token": "...",
//	  "refresh_token": "...",
//	  "expires_at":   "2026-01-01T00:00:00Z"
//	}
//
// Alternatively, ACP_HARNESS_CODEX_ACCESS_TOKEN may be set to skip the
// refresh dance.
package codexoauth

import (
	"context"
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

// DefaultTokenURL is the OpenAI OAuth refresh endpoint used by the Codex
// CLI. It can be overridden via ACP_HARNESS_CODEX_TOKEN_URL.
const DefaultTokenURL = "https://auth.openai.com/oauth/token"

// DefaultClientID is a generic Codex-like public client ID, used only if
// the token file does not record one. Override with
// ACP_HARNESS_CODEX_CLIENT_ID.
const DefaultClientID = "app_codex_default"

// Token is the cached Codex OAuth token.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	ClientID     string    `json:"client_id,omitempty"`
	TokenURL     string    `json:"token_url,omitempty"`
}

// Resolver caches the parsed token and performs refresh-on-demand.
type Resolver struct {
	path       string
	httpClient *http.Client
	envPrefix  string
	label      string

	mu  sync.Mutex
	tok *Token
}

// New constructs a Resolver. tokenPath is the cache file location; if
// empty, the default location is used.
func New(tokenPath string) *Resolver {
	return NewForFlavour("codex", tokenPath)
}

// NewForFlavour constructs a Resolver for "codex" or "openai". The token
// format and refresh flow are the same; env var names and default cache file
// names differ.
func NewForFlavour(flavour, tokenPath string) *Resolver {
	flavour = strings.ToLower(strings.TrimSpace(flavour))
	if flavour == "" {
		flavour = "codex"
	}
	envPrefix := "ACP_HARNESS_" + strings.ToUpper(flavour)
	fileName := flavour + "-token.json"
	if tokenPath == "" {
		tokenPath = defaultTokenPath(envPrefix+"_TOKEN_FILE", fileName)
		if flavour == "codex" && !fileExists(tokenPath) {
			if p := defaultCodexAuthPath(); fileExists(p) {
				tokenPath = p
			}
		}
	}
	return &Resolver{
		path:       tokenPath,
		httpClient: http.DefaultClient,
		envPrefix:  envPrefix,
		label:      flavour,
	}
}

// DefaultTokenPath returns the on-disk token file path according to env
// variables and XDG defaults.
func DefaultTokenPath() string {
	return defaultTokenPath("ACP_HARNESS_CODEX_TOKEN_FILE", "codex-token.json")
}

func defaultTokenPath(envName, fileName string) string {
	if v := os.Getenv(envName); v != "" {
		return v
	}
	if h := os.Getenv("XDG_CONFIG_HOME"); h != "" {
		return filepath.Join(h, "acp-harness", fileName)
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "acp-harness", fileName)
	}
	return ""
}

// Resolve returns a valid access token, refreshing if needed. The
// returned string can be used directly as a Bearer token.
func (r *Resolver) Resolve(ctx context.Context) (string, error) {
	// Highest-priority override: explicit access token from env.
	if v := os.Getenv(r.env("ACCESS_TOKEN")); v != "" {
		return v, nil
	}
	tok, err := r.loadCached()
	if err != nil {
		return "", err
	}
	if !tok.expiringSoon() {
		return tok.AccessToken, nil
	}
	if tok.RefreshToken == "" {
		if v := os.Getenv(r.env("REFRESH_TOKEN")); v != "" {
			tok.RefreshToken = v
		} else {
			return "", fmt.Errorf("%s oauth: access token expired and no refresh token available", r.label)
		}
	}
	refreshed, err := r.refresh(ctx, tok)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	r.tok = refreshed
	r.mu.Unlock()
	_ = r.persist(refreshed)
	return refreshed.AccessToken, nil
}

func (r *Resolver) loadCached() (*Token, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tok != nil {
		return r.tok, nil
	}
	if r.path == "" || !fileExists(r.path) {
		if rt := os.Getenv(r.env("REFRESH_TOKEN")); rt != "" {
			r.tok = &Token{RefreshToken: rt}
			return r.tok, nil
		}
		return nil, fmt.Errorf("%s oauth: token file not found at %s and no %s set", r.label, r.path, r.env("REFRESH_TOKEN"))
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		return nil, err
	}
	var t Token
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("%s oauth: parse %s: %w", r.label, r.path, err)
	}
	if t.AccessToken == "" && t.RefreshToken == "" {
		var nested struct {
			Tokens struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
				IDToken      string `json:"id_token"`
				AccountID    string `json:"account_id"`
			} `json:"tokens"`
			LastRefresh string `json:"last_refresh"`
		}
		if err := json.Unmarshal(data, &nested); err != nil {
			return nil, fmt.Errorf("%s oauth: parse %s: %w", r.label, r.path, err)
		}
		t.AccessToken = nested.Tokens.AccessToken
		t.RefreshToken = nested.Tokens.RefreshToken
	}
	r.tok = &t
	return r.tok, nil
}

func (r *Resolver) persist(t *Token) error {
	if r.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.path, b, 0o600)
}

func (r *Resolver) refresh(ctx context.Context, t *Token) (*Token, error) {
	tokenURL := t.TokenURL
	if v := os.Getenv(r.env("TOKEN_URL")); v != "" {
		tokenURL = v
	}
	if tokenURL == "" {
		tokenURL = DefaultTokenURL
	}
	clientID := t.ClientID
	if v := os.Getenv(r.env("CLIENT_ID")); v != "" {
		clientID = v
	}
	if clientID == "" {
		clientID = DefaultClientID
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", t.RefreshToken)
	form.Set("client_id", clientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s oauth: refresh request: %w", r.label, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s oauth: refresh failed: HTTP %d: %s", r.label, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("%s oauth: parse response: %w", r.label, err)
	}
	if payload.AccessToken == "" {
		return nil, fmt.Errorf("%s oauth: refresh response missing access_token", r.label)
	}
	expiry := time.Now().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second)
	if payload.ExpiresIn == 0 {
		// Default to 1 hour, mirroring OpenAI's typical token TTL.
		expiry = time.Now().UTC().Add(time.Hour)
	}
	out := &Token{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ExpiresAt:    expiry,
		ClientID:     clientID,
		TokenURL:     tokenURL,
	}
	if out.RefreshToken == "" {
		out.RefreshToken = t.RefreshToken
	}
	return out, nil
}

func (t *Token) expiringSoon() bool {
	if t.AccessToken == "" {
		return true
	}
	if t.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().UTC().Add(60 * time.Second).After(t.ExpiresAt)
}

func fileExists(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

func defaultCodexAuthPath() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".codex", "auth.json")
	}
	return ""
}

func (r *Resolver) env(suffix string) string {
	if r.envPrefix == "" {
		return "ACP_HARNESS_CODEX_" + suffix
	}
	return r.envPrefix + "_" + suffix
}
