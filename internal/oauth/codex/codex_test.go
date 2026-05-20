package codexoauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeToken(t *testing.T, path string, tok Token) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(tok)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceLoginSavesToken(t *testing.T) {
	var sawClientID bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			var req map[string]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req["client_id"] == DefaultClientID {
				sawClientID = true
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user_code":      "ABCD-EFGH",
				"device_auth_id": "device-1",
				"interval":       1,
			})
		case "/api/accounts/deviceauth/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"authorization_code": "auth-code",
				"code_verifier":      "verifier",
			})
		case "/oauth/token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.FormValue("grant_type") != "authorization_code" || r.FormValue("code") != "auth-code" {
				t.Fatalf("form = %v", r.Form)
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

	path := filepath.Join(t.TempDir(), "codex-token.json")
	t.Setenv("AGENTBRIDGE_CODEX_ISSUER", srv.URL)
	t.Setenv("AGENTBRIDGE_CODEX_TOKEN_URL", srv.URL+"/oauth/token")
	r := New(path)
	var out bytes.Buffer
	tok, err := r.DeviceLogin(context.Background(), &out)
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "access" || tok.RefreshToken != "refresh" || !sawClientID {
		t.Fatalf("tok=%+v sawClientID=%v", tok, sawClientID)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("access")) {
		t.Fatalf("token file = %s", data)
	}
	if !strings.Contains(out.String(), "ABCD-EFGH") {
		t.Fatalf("output missing user code: %s", out.String())
	}
}

func TestResolveReturnsCachedTokenWhenFresh(t *testing.T) {
	t.Setenv("ACP_HARNESS_CODEX_ACCESS_TOKEN", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "tok.json")
	writeToken(t, path, Token{
		AccessToken:  "good-token",
		RefreshToken: "rt",
		ExpiresAt:    time.Now().Add(2 * time.Hour),
	})
	r := New(path)
	got, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "good-token" {
		t.Errorf("got %q", got)
	}
}

func TestResolveRefreshesExpiredToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("grant_type") != "refresh_token" {
			t.Errorf("grant_type=%q", r.FormValue("grant_type"))
		}
		if r.FormValue("refresh_token") != "stale-rt" {
			t.Errorf("refresh_token=%q", r.FormValue("refresh_token"))
		}
		fmt.Fprintln(w, `{"access_token":"fresh-at","refresh_token":"new-rt","expires_in":3600}`)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "tok.json")
	writeToken(t, path, Token{
		AccessToken:  "expired-at",
		RefreshToken: "stale-rt",
		ExpiresAt:    time.Now().Add(-time.Hour),
		TokenURL:     srv.URL,
	})
	t.Setenv("ACP_HARNESS_CODEX_ACCESS_TOKEN", "")
	r := New(path)
	got, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "fresh-at" {
		t.Errorf("got %q", got)
	}
	// Cache must be updated on disk.
	data, _ := os.ReadFile(path)
	var stored Token
	_ = json.Unmarshal(data, &stored)
	if stored.AccessToken != "fresh-at" || stored.RefreshToken != "new-rt" {
		t.Errorf("token file not updated: %+v", stored)
	}
}

func TestResolveHonoursExplicitEnv(t *testing.T) {
	t.Setenv("ACP_HARNESS_CODEX_ACCESS_TOKEN", "envtoken")
	r := New(filepath.Join(t.TempDir(), "missing.json"))
	got, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "envtoken" {
		t.Errorf("got %q", got)
	}
}

func TestResolveRefreshFromEnvWhenNoFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("refresh_token") != "env-rt" {
			t.Errorf("refresh_token=%q", r.FormValue("refresh_token"))
		}
		fmt.Fprintln(w, `{"access_token":"env-at","expires_in":3600}`)
	}))
	defer srv.Close()
	t.Setenv("ACP_HARNESS_CODEX_ACCESS_TOKEN", "")
	t.Setenv("ACP_HARNESS_CODEX_REFRESH_TOKEN", "env-rt")
	t.Setenv("ACP_HARNESS_CODEX_TOKEN_URL", srv.URL)
	r := New(filepath.Join(t.TempDir(), "missing.json"))
	got, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "env-at" {
		t.Errorf("got %q", got)
	}
}

func TestResolveReadsCodexAuthJSONShape(t *testing.T) {
	t.Setenv("ACP_HARNESS_CODEX_ACCESS_TOKEN", "")
	path := filepath.Join(t.TempDir(), "auth.json")
	body := `{
	  "auth_mode": "chatgpt",
	  "tokens": {
	    "access_token": "nested-at",
	    "refresh_token": "nested-rt",
	    "account_id": "account-123"
	  },
	  "last_refresh": "` + time.Now().UTC().Format(time.RFC3339) + `"
	}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New(path)
	got, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "nested-at" {
		t.Errorf("got %q", got)
	}
	tok, err := r.ResolveToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccountID != "account-123" {
		t.Errorf("account id = %q", tok.AccountID)
	}
}

func TestOpenAIFlavourHonoursOpenAIEnv(t *testing.T) {
	t.Setenv("ACP_HARNESS_OPENAI_ACCESS_TOKEN", "openai-at")
	r := NewForFlavour("openai", filepath.Join(t.TempDir(), "missing.json"))
	got, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "openai-at" {
		t.Errorf("got %q", got)
	}
}
