package codexoauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
