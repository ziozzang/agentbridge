package copilotoauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveExchangesGitHubTokenAndCaches(t *testing.T) {
	var sawIntegration bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer github-token" {
			t.Fatalf("auth = %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Copilot-Integration-Id") == IntegrationID {
			sawIntegration = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "copilot-token;proxy-ep=proxy.example.com;",
			"expires_at": time.Now().Add(time.Hour).Unix(),
		})
	}))
	defer srv.Close()
	t.Setenv("COPILOT_GITHUB_TOKEN", "github-token")
	t.Setenv("AGENTBRIDGE_COPILOT_TOKEN_URL", srv.URL)
	path := filepath.Join(t.TempDir(), "tok.json")
	r := New(path)
	tok, base, err := r.ResolveToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" || base != "https://api.example.com" || !sawIntegration {
		t.Fatalf("tok=%q base=%q sawIntegration=%v", tok, base, sawIntegration)
	}
	got, base, err := r.ResolveToken(context.Background())
	if err != nil || got != tok || base != "https://api.example.com" {
		t.Fatalf("cached tok=%q base=%q err=%v", got, base, err)
	}
}

func TestResolvePrefersDirectCopilotToken(t *testing.T) {
	t.Setenv("COPILOT_API_TOKEN", "direct-token")
	t.Setenv("COPILOT_GITHUB_TOKEN", "")
	tok, base, err := New(filepath.Join(t.TempDir(), "missing.json")).ResolveToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "direct-token" || base != DefaultBaseURL {
		t.Fatalf("tok=%q base=%q", tok, base)
	}
}
