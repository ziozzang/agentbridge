package xaioauth

import (
	"bytes"
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

func TestParseManualCallback(t *testing.T) {
	cb, err := parseManualCallback("http://127.0.0.1:56121/callback?code=abc&state=xyz")
	if err != nil {
		t.Fatal(err)
	}
	if cb.Code != "abc" || cb.State != "xyz" {
		t.Fatalf("callback = %+v", cb)
	}
	cb, err = parseManualCallback("code=def&state=uvw")
	if err != nil {
		t.Fatal(err)
	}
	if cb.Code != "def" || cb.State != "uvw" {
		t.Fatalf("callback = %+v", cb)
	}
	cb, err = parseManualCallback("raw-code")
	if err != nil {
		t.Fatal(err)
	}
	if cb.Code != "raw-code" {
		t.Fatalf("callback = %+v", cb)
	}
}

func TestManualLoginAcceptsRawCode(t *testing.T) {
	var sawCodeVerifier bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authorization_endpoint": "https://auth.x.ai/oauth2/authorize",
				"token_endpoint":         "https://auth.x.ai/oauth2/token",
			})
		case "/oauth2/token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.FormValue("code") != "raw-code" {
				t.Fatalf("form = %v", r.Form)
			}
			if r.FormValue("code_verifier") != "" && r.FormValue("code_challenge") != "" {
				sawCodeVerifier = true
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "access",
				"refresh_token": "refresh",
				"expires_in":    3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "auth.json")
	r := New(path)
	r.httpClient = rewriteHostClient(srv)
	var out bytes.Buffer
	tok, err := r.ManualLogin(context.Background(), strings.NewReader("raw-code\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "access" || tok.RefreshToken != "refresh" || !sawCodeVerifier {
		t.Fatalf("tok=%+v sawCodeVerifier=%v", tok, sawCodeVerifier)
	}
	if !strings.Contains(out.String(), "xAI code/callback") {
		t.Fatalf("output = %s", out.String())
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
