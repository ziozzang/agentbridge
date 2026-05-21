// Package copilotoauth resolves short-lived GitHub Copilot API tokens.
//
// The provider config can use `api_key: oauth:github-copilot`. AgentBridge
// then accepts an already-exchanged Copilot API token from the environment or
// exchanges a GitHub token at GitHub's Copilot token endpoint and caches the
// result.
package copilotoauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultTokenURL = "https://api.github.com/copilot_internal/v2/token"
	DefaultBaseURL  = "https://api.individual.githubcopilot.com"

	EditorVersion       = "vscode/1.107.0"
	EditorPluginVersion = "copilot-chat/0.35.0"
	UserAgent           = "GitHubCopilotChat/0.35.0"
	GitHubAPIVersion    = "2025-04-01"
	IntegrationID       = "vscode-chat"
)

type Token struct {
	Token         string `json:"token"`
	ExpiresAt     int64  `json:"expiresAt"`
	UpdatedAt     int64  `json:"updatedAt"`
	IntegrationID string `json:"integrationId,omitempty"`
	BaseURL       string `json:"baseUrl,omitempty"`
}

type Resolver struct {
	path       string
	httpClient *http.Client
	mu         sync.Mutex
	tok        *Token
}

func New(tokenPath string) *Resolver {
	if tokenPath == "" {
		tokenPath = defaultTokenPath()
	}
	return &Resolver{path: tokenPath, httpClient: http.DefaultClient}
}

func DefaultHeaders() map[string]string {
	return map[string]string{
		"Accept-Encoding":        "identity",
		"Editor-Version":         EditorVersion,
		"Editor-Plugin-Version":  EditorPluginVersion,
		"User-Agent":             UserAgent,
		"Copilot-Integration-Id": IntegrationID,
		"Openai-Organization":    "github-copilot",
		"x-initiator":            "user",
	}
}

func (r *Resolver) Resolve(ctx context.Context) (*Token, error) {
	if v := envFirst("COPILOT_API_TOKEN", "GITHUB_COPILOT_API_TOKEN", "AGENTBRIDGE_COPILOT_API_TOKEN"); v != "" {
		return &Token{Token: v, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), BaseURL: deriveBaseURL(v)}, nil
	}
	if tok, ok := r.cached(); ok {
		return tok, nil
	}
	githubToken := envFirst("COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN")
	if githubToken == "" {
		return nil, fmt.Errorf("github-copilot oauth: set COPILOT_API_TOKEN or COPILOT_GITHUB_TOKEN/GH_TOKEN/GITHUB_TOKEN")
	}
	tok, err := r.exchange(ctx, githubToken)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.tok = tok
	r.mu.Unlock()
	_ = r.persist(tok)
	return tok, nil
}

func (r *Resolver) ResolveToken(ctx context.Context) (string, string, error) {
	tok, err := r.Resolve(ctx)
	if err != nil {
		return "", "", err
	}
	return tok.Token, firstNonEmpty(tok.BaseURL, deriveBaseURL(tok.Token), DefaultBaseURL), nil
}

func (r *Resolver) cached() (*Token, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tok != nil && usable(r.tok) {
		return r.tok, true
	}
	raw, err := os.ReadFile(r.path)
	if err != nil {
		return nil, false
	}
	var tok Token
	if json.Unmarshal(raw, &tok) != nil || !usable(&tok) {
		return nil, false
	}
	if tok.BaseURL == "" {
		tok.BaseURL = deriveBaseURL(tok.Token)
	}
	r.tok = &tok
	return &tok, true
}

func usable(tok *Token) bool {
	if tok == nil || strings.TrimSpace(tok.Token) == "" || tok.IntegrationID != IntegrationID {
		return false
	}
	return time.Until(time.UnixMilli(tok.ExpiresAt)) > 5*time.Minute
}

func (r *Resolver) exchange(ctx context.Context, githubToken string) (*Token, error) {
	url := envFirst("AGENTBRIDGE_COPILOT_TOKEN_URL", "COPILOT_TOKEN_URL")
	if url == "" {
		url = DefaultTokenURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+githubToken)
	req.Header.Set("Copilot-Integration-Id", IntegrationID)
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Editor-Version", EditorVersion)
	req.Header.Set("Editor-Plugin-Version", EditorPluginVersion)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("X-Github-Api-Version", GitHubAPIVersion)
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github-copilot oauth: token exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("github-copilot oauth: token exchange failed: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Token     string `json:"token"`
		ExpiresAt any    `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("github-copilot oauth: parse token response: %w", err)
	}
	if strings.TrimSpace(body.Token) == "" {
		return nil, fmt.Errorf("github-copilot oauth: token response missing token")
	}
	expiresAt, err := parseExpiresAt(body.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return &Token{
		Token:         body.Token,
		ExpiresAt:     expiresAt,
		UpdatedAt:     time.Now().UnixMilli(),
		IntegrationID: IntegrationID,
		BaseURL:       deriveBaseURL(body.Token),
	}, nil
}

func parseExpiresAt(v any) (int64, error) {
	switch x := v.(type) {
	case float64:
		n := int64(x)
		if n < 100_000_000_000 {
			n *= 1000
		}
		return n, nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("github-copilot oauth: invalid expires_at")
		}
		if n < 100_000_000_000 {
			n *= 1000
		}
		return n, nil
	default:
		return 0, fmt.Errorf("github-copilot oauth: token response missing expires_at")
	}
}

func deriveBaseURL(token string) string {
	lower := strings.ToLower(token)
	idx := strings.Index(lower, "proxy-ep=")
	if idx < 0 {
		return DefaultBaseURL
	}
	raw := token[idx+len("proxy-ep="):]
	if semi := strings.Index(raw, ";"); semi >= 0 {
		raw = raw[:semi]
	}
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	raw = strings.TrimPrefix(raw, "proxy.")
	if raw == "" {
		return DefaultBaseURL
	}
	return "https://api." + raw
}

func (r *Resolver) persist(tok *Token) error {
	if r.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.path, append(b, '\n'), 0o600)
}

func defaultTokenPath() string {
	if v := envFirst("AGENTBRIDGE_COPILOT_TOKEN_FILE", "GITHUB_COPILOT_TOKEN_FILE"); v != "" {
		return v
	}
	if h := os.Getenv("XDG_CONFIG_HOME"); h != "" {
		return filepath.Join(h, "agentbridge", "github-copilot-token.json")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "agentbridge", "github-copilot-token.json")
	}
	return ""
}

func envFirst(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
