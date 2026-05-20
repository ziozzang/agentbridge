package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ziozzang/agentbridge/internal/provider"
	_ "github.com/ziozzang/agentbridge/internal/provider/openaichat"
)

func TestRouterRoutesByModelAndRotatesKeys(t *testing.T) {
	var saw []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		saw = append(saw, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c, err := New(provider.Config{
		Name: "router", Kind: Kind, DefaultModel: "grok",
		Extra: map[string]any{
			"_providers": map[string]provider.Config{
				"xai": {Name: "xai", Kind: "openai-chat", BaseURL: srv.URL, APIKey: "base-key", DefaultModel: "grok-4.3"},
			},
			"routes": []any{
				map[string]any{
					"match":        "grok",
					"provider":     "xai",
					"target_model": "grok-4.3",
					"api_keys":     []any{"k1", "k2"},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		chunks, errs := c.StreamChat(context.Background(), []provider.Message{{Role: "user", Content: "hi"}}, provider.StreamOptions{Model: "grok"})
		for range chunks {
		}
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"Bearer k1", "Bearer k2", "Bearer k1"}
	if fmt.Sprint(saw) != fmt.Sprint(want) {
		t.Fatalf("keys = %v want %v", saw, want)
	}
}

func TestRouterAcceptsStringKeyLists(t *testing.T) {
	t.Setenv("KEY_A", "ka")
	t.Setenv("KEY_B", "kb")
	r := route{}
	data := []byte(`{"provider":"p","match":"m","api_keys":"k1,k2","api_key_envs":"KEY_A;KEY_B"}`)
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(r.APIKeys) != "[k1 k2]" || fmt.Sprint(r.APIKeyEnvs) != "[KEY_A KEY_B]" {
		t.Fatalf("bad parsed keys: keys=%v envs=%v", r.APIKeys, r.APIKeyEnvs)
	}
	if fmt.Sprint(r.keys()) != "[k1 k2 ka kb]" {
		t.Fatalf("resolved keys = %v", r.keys())
	}
}

func TestRouterRetriesNextKeyOnRateLimitBeforeStreaming(t *testing.T) {
	var saw []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		saw = append(saw, auth)
		if auth == "Bearer k1" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"weekly limit reached"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c, err := New(provider.Config{
		Name: "router", Kind: Kind, DefaultModel: "m",
		Extra: map[string]any{
			"_providers": map[string]provider.Config{
				"p": {Name: "p", Kind: "openai-chat", BaseURL: srv.URL, DefaultModel: "m"},
			},
			"routes": []any{map[string]any{
				"match":      "m",
				"provider":   "p",
				"api_keys":   []any{"k1", "k2"},
				"retry_keys": true,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chunks, errs := c.StreamChat(context.Background(), []provider.Message{{Role: "user", Content: "hi"}}, provider.StreamOptions{Model: "m"})
	text := ""
	for ch := range chunks {
		text += ch.Text
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if text != "ok" {
		t.Fatalf("text = %q", text)
	}
	if fmt.Sprint(saw) != "[Bearer k1 Bearer k2]" {
		t.Fatalf("saw = %v", saw)
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, model string
		want       bool
	}{
		{"grok-*", "grok-4.3", true},
		{"*-embed", "jina-embed", true},
		{"glm-*", "grok-4.3", false},
	}
	for _, tc := range cases {
		if got := globMatch(tc.pat, tc.model); got != tc.want {
			t.Fatalf("globMatch(%q,%q)=%v", tc.pat, tc.model, got)
		}
	}
}

func TestResolveTargetModelWildcardCapture(t *testing.T) {
	r := route{Match: "zai:*", TargetModel: "$1"}
	if got := resolveTargetModel(r, "zai:glm-5.1", "fallback"); got != "glm-5.1" {
		t.Fatalf("target = %q", got)
	}
}
