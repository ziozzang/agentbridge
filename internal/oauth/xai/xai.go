// Package xaioauth resolves xAI Grok OAuth bearer tokens.
//
// It intentionally mirrors the Hermes Agent xai-oauth flow enough for
// AgentBridge runtime use:
//   - read a flat AgentBridge token file, or
//   - read ~/.grok/auth.json providers["xai-oauth"] entry, or
//   - read Hermes' ~/.hermes/auth.json providers["xai-oauth"] entry as a fallback, and
//   - refresh expiring JWT access tokens through xAI's OIDC token endpoint, and
//   - bootstrap new tokens with xAI's OAuth device-code flow.
package xaioauth

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
	DefaultScope        = "openid profile email offline_access grok-cli:access api:access"
	DefaultRedirectHost = "127.0.0.1"
	DefaultRedirectPort = 56121
	DefaultRedirectPath = "/callback"
	deviceGrantType     = "urn:ietf:params:oauth:grant-type:device_code"
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

// Discovery is the subset of xAI's OpenID metadata AgentBridge needs.
type Discovery struct {
	TokenEndpoint               string `json:"token_endpoint"`
	AuthorizationEndpoint       string `json:"authorization_endpoint"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
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

// DeviceLogin performs xAI's browser-assisted device-code flow and saves the
// resulting token to the resolver path. The user opens the printed URL, enters
// the code if needed, and AgentBridge polls until xAI issues tokens.
func (r *Resolver) DeviceLogin(ctx context.Context, out io.Writer) (*Token, error) {
	disco, err := r.discover(ctx)
	if err != nil {
		return nil, err
	}
	if disco.DeviceAuthorizationEndpoint == "" {
		return nil, fmt.Errorf("xai oauth: discovery missing device_authorization_endpoint")
	}
	if err := validateXAIEndpoint(disco.DeviceAuthorizationEndpoint); err != nil {
		return nil, err
	}
	if err := validateXAIEndpoint(disco.TokenEndpoint); err != nil {
		return nil, err
	}
	clientID := envFirst("AGENTBRIDGE_XAI_OAUTH_CLIENT_ID", "XAI_OAUTH_CLIENT_ID")
	if clientID == "" {
		clientID = DefaultClientID
	}
	scope := envFirst("AGENTBRIDGE_XAI_OAUTH_SCOPE", "XAI_OAUTH_SCOPE")
	if scope == "" {
		scope = DefaultScope
	}
	started, err := r.startDeviceFlow(ctx, disco.DeviceAuthorizationEndpoint, clientID, scope)
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(out, "Open this URL in your browser to authorize Grok OAuth:")
	if started.VerificationURIComplete != "" {
		fmt.Fprintln(out, started.VerificationURIComplete)
	} else {
		fmt.Fprintln(out, started.VerificationURI)
	}
	if started.UserCode != "" {
		fmt.Fprintln(out, "Code:", started.UserCode)
	}
	fmt.Fprintln(out, "Waiting for authorization...")
	tok, err := r.pollDeviceToken(ctx, disco.TokenEndpoint, clientID, started)
	if err != nil {
		return nil, err
	}
	tok.ClientID = clientID
	tok.TokenEndpoint = disco.TokenEndpoint
	if err := r.persistHermes(tok); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.tok = tok
	r.mu.Unlock()
	return tok, nil
}

// LoopbackLogin performs the Hermes-compatible xAI OAuth PKCE loopback flow.
// It prints the authorization URL, waits on 127.0.0.1 for the callback, then
// saves ~/.grok/auth.json in the Hermes-compatible provider-entry shape.
func (r *Resolver) LoopbackLogin(ctx context.Context, out io.Writer) (*Token, error) {
	disco, err := r.discover(ctx)
	if err != nil {
		return nil, err
	}
	if disco.AuthorizationEndpoint == "" {
		return nil, fmt.Errorf("xai oauth: discovery missing authorization_endpoint")
	}
	if err := validateXAIEndpoint(disco.AuthorizationEndpoint); err != nil {
		return nil, err
	}
	if err := validateXAIEndpoint(disco.TokenEndpoint); err != nil {
		return nil, err
	}
	clientID := envFirst("AGENTBRIDGE_XAI_OAUTH_CLIENT_ID", "XAI_OAUTH_CLIENT_ID")
	if clientID == "" {
		clientID = DefaultClientID
	}
	scope := envFirst("AGENTBRIDGE_XAI_OAUTH_SCOPE", "XAI_OAUTH_SCOPE")
	if scope == "" {
		scope = DefaultScope
	}
	codeVerifier, err := randomURLSafe(64)
	if err != nil {
		return nil, err
	}
	codeChallenge := pkceChallenge(codeVerifier)
	state, err := randomURLSafe(24)
	if err != nil {
		return nil, err
	}
	nonce, err := randomURLSafe(24)
	if err != nil {
		return nil, err
	}
	callback, redirectURI, shutdown, err := startLoopbackServer()
	if err != nil {
		return nil, err
	}
	defer shutdown()

	authURL := buildAuthorizeURL(disco.AuthorizationEndpoint, clientID, redirectURI, scope, codeChallenge, state, nonce)
	fmt.Fprintln(out, "Open this URL to authorize AgentBridge with xAI:")
	fmt.Fprintln(out, authURL)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Waiting for callback on "+redirectURI)
	fmt.Fprintln(out, "If this runs on a remote host, forward local port 56121 or paste the URL into a browser on this host.")

	var cb oauthCallback
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case cb = <-callback:
	case <-time.After(3 * time.Minute):
		return nil, fmt.Errorf("xai oauth: timed out waiting for callback")
	}
	if cb.Error != "" {
		return nil, fmt.Errorf("xai oauth: authorization failed: %s", firstNonEmpty(cb.ErrorDescription, cb.Error))
	}
	if cb.State != state {
		return nil, fmt.Errorf("xai oauth: state mismatch")
	}
	if cb.Code == "" {
		return nil, fmt.Errorf("xai oauth: callback missing code")
	}
	tok, err := r.exchangeAuthorizationCode(ctx, disco.TokenEndpoint, clientID, cb.Code, redirectURI, codeVerifier, codeChallenge)
	if err != nil {
		return nil, err
	}
	tok.ClientID = clientID
	tok.TokenEndpoint = disco.TokenEndpoint
	if err := r.persistHermes(tok); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.tok = tok
	r.mu.Unlock()
	return tok, nil
}

// ManualLogin runs the same xAI PKCE flow as LoopbackLogin, but skips the
// local callback listener. It is for remote shells where the browser cannot
// reach 127.0.0.1 on the server. Paste either the full callback URL, the query
// string, or the code shown by xAI.
func (r *Resolver) ManualLogin(ctx context.Context, in io.Reader, out io.Writer) (*Token, error) {
	disco, err := r.discover(ctx)
	if err != nil {
		return nil, err
	}
	if disco.AuthorizationEndpoint == "" {
		return nil, fmt.Errorf("xai oauth: discovery missing authorization_endpoint")
	}
	if err := validateXAIEndpoint(disco.AuthorizationEndpoint); err != nil {
		return nil, err
	}
	if err := validateXAIEndpoint(disco.TokenEndpoint); err != nil {
		return nil, err
	}
	clientID := envFirst("AGENTBRIDGE_XAI_OAUTH_CLIENT_ID", "XAI_OAUTH_CLIENT_ID")
	if clientID == "" {
		clientID = DefaultClientID
	}
	scope := envFirst("AGENTBRIDGE_XAI_OAUTH_SCOPE", "XAI_OAUTH_SCOPE")
	if scope == "" {
		scope = DefaultScope
	}
	codeVerifier, err := randomURLSafe(64)
	if err != nil {
		return nil, err
	}
	codeChallenge := pkceChallenge(codeVerifier)
	state, err := randomURLSafe(24)
	if err != nil {
		return nil, err
	}
	nonce, err := randomURLSafe(24)
	if err != nil {
		return nil, err
	}
	redirectURI := fmt.Sprintf("http://%s:%d%s", DefaultRedirectHost, DefaultRedirectPort, DefaultRedirectPath)
	authURL := buildAuthorizeURL(disco.AuthorizationEndpoint, clientID, redirectURI, scope, codeChallenge, state, nonce)
	fmt.Fprintln(out, "Open this URL to authorize AgentBridge with xAI:")
	fmt.Fprintln(out, authURL)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "If xAI says it could not reach the app, paste the displayed code or callback URL below.")
	fmt.Fprint(out, "xAI code/callback: ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	cb, err := parseManualCallback(strings.TrimSpace(line))
	if err != nil {
		return nil, err
	}
	if cb.Error != "" {
		return nil, fmt.Errorf("xai oauth: authorization failed: %s", firstNonEmpty(cb.ErrorDescription, cb.Error))
	}
	if cb.State != "" && cb.State != state {
		return nil, fmt.Errorf("xai oauth: state mismatch")
	}
	if cb.Code == "" {
		return nil, fmt.Errorf("xai oauth: missing code")
	}
	tok, err := r.exchangeAuthorizationCode(ctx, disco.TokenEndpoint, clientID, cb.Code, redirectURI, codeVerifier, codeChallenge)
	if err != nil {
		return nil, err
	}
	tok.ClientID = clientID
	tok.TokenEndpoint = disco.TokenEndpoint
	if err := r.persistHermes(tok); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.tok = tok
	r.mu.Unlock()
	return tok, nil
}

func parseManualCallback(raw string) (oauthCallback, error) {
	if raw == "" {
		return oauthCallback{}, fmt.Errorf("xai oauth: empty callback/code")
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		u, err := url.Parse(raw)
		if err != nil {
			return oauthCallback{}, err
		}
		return callbackFromValues(u.Query()), nil
	}
	if strings.Contains(raw, "=") {
		raw = strings.TrimPrefix(raw, "?")
		q, err := url.ParseQuery(raw)
		if err != nil {
			return oauthCallback{}, err
		}
		return callbackFromValues(q), nil
	}
	return oauthCallback{Code: raw}, nil
}

func callbackFromValues(q url.Values) oauthCallback {
	return oauthCallback{
		Code:             q.Get("code"),
		State:            q.Get("state"),
		Error:            q.Get("error"),
		ErrorDescription: q.Get("error_description"),
	}
}

type oauthCallback struct {
	Code             string
	State            string
	Error            string
	ErrorDescription string
}

func startLoopbackServer() (<-chan oauthCallback, string, func(), error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", DefaultRedirectHost, DefaultRedirectPort))
	if err != nil {
		ln, err = net.Listen("tcp", DefaultRedirectHost+":0")
		if err != nil {
			return nil, "", nil, err
		}
	}
	ch := make(chan oauthCallback, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(DefaultRedirectPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		cb := oauthCallback{
			Code:             q.Get("code"),
			State:            q.Get("state"),
			Error:            q.Get("error"),
			ErrorDescription: q.Get("error_description"),
		}
		select {
		case ch <- cb:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<html><body><h1>xAI authorization received.</h1>You can close this tab.</body></html>")
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	port := ln.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://%s:%d%s", DefaultRedirectHost, port, DefaultRedirectPath)
	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	return ch, redirectURI, shutdown, nil
}

func buildAuthorizeURL(endpoint, clientID, redirectURI, scope, challenge, state, nonce string) string {
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", scope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("plan", "generic")
	q.Set("referrer", "agentbridge")
	return endpoint + "?" + q.Encode()
}

func (r *Resolver) exchangeAuthorizationCode(ctx context.Context, endpoint, clientID, code, redirectURI, verifier, challenge string) (*Token, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)
	form.Set("code_challenge", challenge)
	form.Set("code_challenge_method", "S256")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xai oauth: token exchange: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("xai oauth: token exchange failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("xai oauth: parse token exchange response: %w", err)
	}
	if payload.AccessToken == "" || payload.RefreshToken == "" {
		return nil, fmt.Errorf("xai oauth: token exchange response missing access_token or refresh_token")
	}
	tok := &Token{AccessToken: payload.AccessToken, RefreshToken: payload.RefreshToken}
	if payload.ExpiresIn > 0 {
		tok.ExpiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}
	return tok, nil
}

type deviceStart struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func (r *Resolver) startDeviceFlow(ctx context.Context, endpoint, clientID, scope string) (*deviceStart, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", scope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xai oauth: device authorization request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("xai oauth: device authorization failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload deviceStart
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("xai oauth: parse device authorization response: %w", err)
	}
	if payload.DeviceCode == "" || payload.VerificationURI == "" {
		return nil, fmt.Errorf("xai oauth: device authorization response missing device_code or verification_uri")
	}
	if payload.Interval <= 0 {
		payload.Interval = 5
	}
	return &payload, nil
}

func (r *Resolver) pollDeviceToken(ctx context.Context, endpoint, clientID string, started *deviceStart) (*Token, error) {
	deadline := time.Now().Add(time.Duration(started.ExpiresIn) * time.Second)
	if started.ExpiresIn <= 0 {
		deadline = time.Now().Add(10 * time.Minute)
	}
	interval := time.Duration(started.Interval) * time.Second
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("xai oauth: device code expired")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		tok, waitMore, slowDown, err := r.tryDeviceToken(ctx, endpoint, clientID, started.DeviceCode)
		if err != nil {
			return nil, err
		}
		if tok != nil {
			return tok, nil
		}
		if slowDown {
			interval += 5 * time.Second
		}
		if !waitMore {
			return nil, fmt.Errorf("xai oauth: device authorization did not complete")
		}
	}
}

func (r *Resolver) tryDeviceToken(ctx context.Context, endpoint, clientID, deviceCode string) (*Token, bool, bool, error) {
	form := url.Values{}
	form.Set("grant_type", deviceGrantType)
	form.Set("client_id", clientID)
	form.Set("device_code", deviceCode)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, false, false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, false, false, fmt.Errorf("xai oauth: device token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 == 2 {
		var payload struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, false, false, fmt.Errorf("xai oauth: parse device token response: %w", err)
		}
		if payload.AccessToken == "" {
			return nil, false, false, fmt.Errorf("xai oauth: device token response missing access_token")
		}
		tok := &Token{AccessToken: payload.AccessToken, RefreshToken: payload.RefreshToken}
		if payload.ExpiresIn > 0 {
			tok.ExpiresAt = time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)
		}
		return tok, false, false, nil
	}
	var oauthErr struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &oauthErr)
	switch oauthErr.Error {
	case "authorization_pending":
		return nil, true, false, nil
	case "slow_down":
		return nil, true, true, nil
	case "expired_token":
		return nil, false, false, fmt.Errorf("xai oauth: device code expired")
	case "access_denied":
		return nil, false, false, fmt.Errorf("xai oauth: authorization denied")
	}
	msg := strings.TrimSpace(string(body))
	if oauthErr.Error != "" {
		msg = oauthErr.Error
		if oauthErr.ErrorDescription != "" {
			msg += ": " + oauthErr.ErrorDescription
		}
	}
	return nil, false, false, fmt.Errorf("xai oauth: device token failed: HTTP %d: %s", resp.StatusCode, msg)
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
	disco, err := r.discover(ctx)
	if err != nil {
		return "", err
	}
	if disco.TokenEndpoint == "" {
		return "", fmt.Errorf("xai oauth: discovery missing token_endpoint")
	}
	if err := validateXAIEndpoint(disco.TokenEndpoint); err != nil {
		return "", err
	}
	return disco.TokenEndpoint, nil
}

func (r *Resolver) discover(ctx context.Context) (*Discovery, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DefaultDiscoveryURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xai oauth: discovery request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("xai oauth: discovery failed: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload Discovery
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
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

func (r *Resolver) persistHermes(t *Token) error {
	if r.path == "" {
		return fmt.Errorf("xai oauth: no token file path configured")
	}
	state := map[string]any{
		"providers": map[string]any{
			DefaultProviderID: map[string]any{
				"tokens": map[string]any{
					"access_token":  t.AccessToken,
					"refresh_token": t.RefreshToken,
				},
				"discovery": map[string]any{
					"token_endpoint": t.TokenEndpoint,
				},
				"last_refresh": time.Now().UTC().Format(time.RFC3339),
			},
		},
	}
	return writeJSON0600(r.path, state)
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

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.TrimRight(base64.URLEncoding.EncodeToString(b), "="), nil
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return strings.TrimRight(base64.URLEncoding.EncodeToString(sum[:]), "=")
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
