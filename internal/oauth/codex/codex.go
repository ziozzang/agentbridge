// Package codexoauth implements the OpenAI Codex OAuth-style token
// resolver. When a provider config uses `api_key: oauth:codex`, AgentBridge
// resolves the live access token by reading the cached token JSON (Codex
// CLI format) and, when it has expired, exchanging the refresh token at
// OpenAI's OAuth endpoint.
//
// The expected cache path is:
//
//	$AGENTBRIDGE_CODEX_TOKEN_FILE        if set, or else
//	$ACP_HARNESS_CODEX_TOKEN_FILE        if set, or else
//	$XDG_CONFIG_HOME/agentbridge/codex-token.json or else
//	~/.config/agentbridge/codex-token.json
//
// Token file shape:
//
//	{
//	  "access_token": "...",
//	  "refresh_token": "...",
//	  "expires_at":   "2026-01-01T00:00:00Z"
//	}
//
// Alternatively, AGENTBRIDGE_CODEX_ACCESS_TOKEN may be set to skip the
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
// CLI. It can be overridden via AGENTBRIDGE_CODEX_TOKEN_URL.
const DefaultTokenURL = "https://auth.openai.com/oauth/token"

// DefaultClientID is the public Codex OAuth client ID used by Codex/Hermes
// device-code login, used only if the token file does not record one. Override with
// AGENTBRIDGE_CODEX_CLIENT_ID.
const DefaultClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

const defaultIssuer = "https://auth.openai.com"

// Token is the cached Codex OAuth token.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	ClientID     string    `json:"client_id,omitempty"`
	TokenURL     string    `json:"token_url,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
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
	envPrefix := "AGENTBRIDGE_" + strings.ToUpper(flavour)
	fileName := flavour + "-token.json"
	if tokenPath == "" {
		tokenPath = defaultTokenPath(envPrefix+"_TOKEN_FILE", "ACP_HARNESS_"+strings.ToUpper(flavour)+"_TOKEN_FILE", fileName)
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
	return defaultTokenPath("AGENTBRIDGE_CODEX_TOKEN_FILE", "ACP_HARNESS_CODEX_TOKEN_FILE", "codex-token.json")
}

func defaultTokenPath(envName, legacyEnvName, fileName string) string {
	if v := envFirst(envName, legacyEnvName); v != "" {
		return v
	}
	if h := os.Getenv("XDG_CONFIG_HOME"); h != "" {
		return filepath.Join(h, "agentbridge", fileName)
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "agentbridge", fileName)
	}
	return ""
}

// Resolve returns a valid access token, refreshing if needed. The
// returned string can be used directly as a Bearer token.
func (r *Resolver) Resolve(ctx context.Context) (string, error) {
	tok, err := r.ResolveToken(ctx)
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// ResolveToken returns a valid access token plus non-secret metadata such as
// ChatGPT account id, refreshing the token when needed.
func (r *Resolver) ResolveToken(ctx context.Context) (*Token, error) {
	// Highest-priority override: explicit access token from env.
	if v := envFirst(r.env("ACCESS_TOKEN"), r.legacyEnv("ACCESS_TOKEN")); v != "" {
		return &Token{AccessToken: v, AccountID: envFirst(r.env("ACCOUNT_ID"), r.legacyEnv("ACCOUNT_ID"))}, nil
	}
	tok, err := r.loadCached()
	if err != nil {
		return nil, err
	}
	if v := envFirst(r.env("ACCOUNT_ID"), r.legacyEnv("ACCOUNT_ID")); v != "" {
		tok.AccountID = v
	}
	if !tok.expiringSoon() {
		return tok, nil
	}
	if tok.RefreshToken == "" {
		if v := envFirst(r.env("REFRESH_TOKEN"), r.legacyEnv("REFRESH_TOKEN")); v != "" {
			tok.RefreshToken = v
		} else {
			return nil, fmt.Errorf("%s oauth: access token expired and no refresh token available", r.label)
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

// DeviceLogin runs OpenAI Codex's browser-assisted device-code flow and saves
// the resulting OAuth tokens to the resolver path.
func (r *Resolver) DeviceLogin(ctx context.Context, out io.Writer) (*Token, error) {
	clientID := envFirst(r.env("CLIENT_ID"), r.legacyEnv("CLIENT_ID"))
	if clientID == "" {
		clientID = DefaultClientID
	}
	issuer := envFirst(r.env("ISSUER"), r.legacyEnv("ISSUER"))
	if issuer == "" {
		issuer = defaultIssuer
	}
	start, err := r.requestDeviceCode(ctx, issuer, clientID)
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(out, "To continue, follow these steps:")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  1. Open this URL in your browser:")
	fmt.Fprintf(out, "     %s/codex/device\n\n", issuer)
	fmt.Fprintln(out, "  2. Enter this code:")
	fmt.Fprintf(out, "     %s\n\n", start.UserCode)
	fmt.Fprintln(out, "Waiting for sign-in... (press Ctrl+C to cancel)")
	auth, err := r.pollDeviceAuth(ctx, issuer, start)
	if err != nil {
		return nil, err
	}
	tok, err := r.exchangeDeviceAuth(ctx, issuer, clientID, auth)
	if err != nil {
		return nil, err
	}
	tok.ClientID = clientID
	if err := r.persist(tok); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.tok = tok
	r.mu.Unlock()
	return tok, nil
}

type deviceCodeStart struct {
	UserCode     string
	DeviceAuthID string
	Interval     int
}

type deviceAuthCode struct {
	AuthorizationCode string `json:"authorization_code"`
	CodeVerifier      string `json:"code_verifier"`
}

func (r *Resolver) requestDeviceCode(ctx context.Context, issuer, clientID string) (*deviceCodeStart, error) {
	body, _ := json.Marshal(map[string]string{"client_id": clientID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(issuer, "/")+"/api/accounts/deviceauth/usercode", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s oauth: device code request: %w", r.label, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s oauth: device code failed: HTTP %d: %s", r.label, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var payload struct {
		UserCode     string `json:"user_code"`
		DeviceAuthID string `json:"device_auth_id"`
		Interval     any    `json:"interval"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("%s oauth: parse device code response: %w", r.label, err)
	}
	out := deviceCodeStart{
		UserCode:     payload.UserCode,
		DeviceAuthID: payload.DeviceAuthID,
		Interval:     parseInterval(payload.Interval),
	}
	if out.UserCode == "" || out.DeviceAuthID == "" {
		return nil, fmt.Errorf("%s oauth: device code response missing user_code or device_auth_id", r.label)
	}
	if out.Interval <= 0 {
		out.Interval = 5
	}
	return &out, nil
}

func parseInterval(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		var n int
		if _, err := fmt.Sscanf(x, "%d", &n); err == nil {
			return n
		}
	}
	return 0
}

func (r *Resolver) pollDeviceAuth(ctx context.Context, issuer string, start *deviceCodeStart) (*deviceAuthCode, error) {
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(start.Interval) * time.Second):
		}
		body, _ := json.Marshal(map[string]string{
			"device_auth_id": start.DeviceAuthID,
			"user_code":      start.UserCode,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(issuer, "/")+"/api/accounts/deviceauth/token", strings.NewReader(string(body)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		resp, err := r.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%s oauth: device auth poll: %w", r.label, err)
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var out deviceAuthCode
			if err := json.Unmarshal(raw, &out); err != nil {
				return nil, fmt.Errorf("%s oauth: parse device auth response: %w", r.label, err)
			}
			if out.AuthorizationCode == "" || out.CodeVerifier == "" {
				return nil, fmt.Errorf("%s oauth: device auth response missing authorization_code or code_verifier", r.label)
			}
			return &out, nil
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			continue
		}
		return nil, fmt.Errorf("%s oauth: device auth poll failed: HTTP %d: %s", r.label, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return nil, fmt.Errorf("%s oauth: login timed out after 15 minutes", r.label)
}

func (r *Resolver) exchangeDeviceAuth(ctx context.Context, issuer, clientID string, auth *deviceAuthCode) (*Token, error) {
	tokenURL := envFirst(r.env("TOKEN_URL"), r.legacyEnv("TOKEN_URL"))
	if tokenURL == "" {
		tokenURL = DefaultTokenURL
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", auth.AuthorizationCode)
	form.Set("redirect_uri", strings.TrimRight(issuer, "/")+"/deviceauth/callback")
	form.Set("client_id", clientID)
	form.Set("code_verifier", auth.CodeVerifier)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s oauth: token exchange: %w", r.label, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s oauth: token exchange failed: HTTP %d: %s", r.label, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("%s oauth: parse token exchange response: %w", r.label, err)
	}
	if payload.AccessToken == "" {
		return nil, fmt.Errorf("%s oauth: token exchange response missing access_token", r.label)
	}
	expiry := time.Now().UTC().Add(time.Hour)
	if payload.ExpiresIn > 0 {
		expiry = time.Now().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	return &Token{
		AccessToken:  payload.AccessToken,
		RefreshToken: payload.RefreshToken,
		ExpiresAt:    expiry,
		ClientID:     clientID,
		TokenURL:     tokenURL,
	}, nil
}

func (r *Resolver) loadCached() (*Token, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tok != nil {
		return r.tok, nil
	}
	if r.path == "" || !fileExists(r.path) {
		if rt := envFirst(r.env("REFRESH_TOKEN"), r.legacyEnv("REFRESH_TOKEN")); rt != "" {
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
		t.AccountID = nested.Tokens.AccountID
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
	if v := envFirst(r.env("TOKEN_URL"), r.legacyEnv("TOKEN_URL")); v != "" {
		tokenURL = v
	}
	if tokenURL == "" {
		tokenURL = DefaultTokenURL
	}
	clientID := t.ClientID
	if v := envFirst(r.env("CLIENT_ID"), r.legacyEnv("CLIENT_ID")); v != "" {
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
		AccountID:    t.AccountID,
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
		return "AGENTBRIDGE_CODEX_" + suffix
	}
	return r.envPrefix + "_" + suffix
}

func (r *Resolver) legacyEnv(suffix string) string {
	if r.envPrefix == "" {
		return "ACP_HARNESS_CODEX_" + suffix
	}
	return "ACP_HARNESS_" + strings.TrimPrefix(r.envPrefix, "AGENTBRIDGE_") + "_" + suffix
}

func envFirst(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}
