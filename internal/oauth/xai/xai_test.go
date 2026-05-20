package xaioauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseHermesStyleAuthStore(t *testing.T) {
	raw := []byte(`{"providers":{"xai-oauth":{"tokens":{"access_token":"access","refresh_token":"refresh"},"discovery":{"token_endpoint":"https://auth.x.ai/oauth2/token"}}}}`)
	tok, err := parseToken(raw)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "access" || tok.RefreshToken != "refresh" || tok.TokenEndpoint != "https://auth.x.ai/oauth2/token" {
		t.Fatalf("token = %+v", tok)
	}
}

func TestRefreshUsesXAIClientID(t *testing.T) {
	var sawClientID bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("client_id") == DefaultClientID {
			sawClientID = true
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh" {
			t.Fatalf("form = %v", r.Form)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "fresh",
			"refresh_token": "new-refresh",
			"expires_in":    3600,
		})
	}))
	defer srv.Close()

	r := New("")
	r.httpClient = srv.Client()
	tok, err := r.refresh(context.Background(), &Token{
		RefreshToken:  "refresh",
		TokenEndpoint: "http://127.0.0.1/token",
	})
	if err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("expected xAI endpoint guard with test server URL unset, got tok=%+v err=%v", tok, err)
	}

	t.Setenv("AGENTBRIDGE_XAI_OAUTH_TOKEN_URL", "https://auth.x.ai/oauth2/token")
	r.httpClient = rewriteHostClient(srv)
	tok, err = r.refresh(context.Background(), &Token{RefreshToken: "refresh"})
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "fresh" || tok.RefreshToken != "new-refresh" || !sawClientID {
		t.Fatalf("token=%+v sawClientID=%v", tok, sawClientID)
	}
}

func TestDefaultPathPrefersGrokAuthJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, ".grok"), 0o700)
	path := filepath.Join(home, ".grok", "auth.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := defaultTokenPath(); got != path {
		t.Fatalf("path = %q want %q", got, path)
	}
}

func TestJWTExpiry(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	token := "h." + strings.TrimRight(base64.URLEncoding.EncodeToString(payload), "=") + ".s"
	exp, ok := jwtExpiry(token)
	if !ok || time.Until(exp) <= 0 {
		t.Fatalf("exp=%v ok=%v", exp, ok)
	}
}

func rewriteHostClient(srv *httptest.Server) *http.Client {
	client := srv.Client()
	baseTransport := client.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = strings.TrimPrefix(srv.URL, "http://")
		return baseTransport.RoundTrip(req)
	})
	return client
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
