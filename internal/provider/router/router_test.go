package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

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

func TestRouterLimitsConcurrentRequestsPerKey(t *testing.T) {
	var inFlight int32
	var maxSeen int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxSeen)
			if n <= old || atomic.CompareAndSwapInt32(&maxSeen, old, n) {
				break
			}
		}
		<-release
		atomic.AddInt32(&inFlight, -1)
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
				"match":                  "m",
				"provider":               "p",
				"api_keys":               []any{"k1"},
				"max_concurrent_per_key": 1,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	errsDone := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			chunks, errs := c.StreamChat(context.Background(), []provider.Message{{Role: "user", Content: "hi"}}, provider.StreamOptions{Model: "m"})
			for range chunks {
			}
			errsDone <- <-errs
		}()
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&inFlight) == 0 {
		time.Sleep(time.Millisecond)
	}
	if got := atomic.LoadInt32(&inFlight); got != 1 {
		t.Fatalf("inFlight before release = %d, want 1", got)
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-errsDone; err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&maxSeen); got != 1 {
		t.Fatalf("max concurrency = %d, want 1", got)
	}
}

func TestRouterFallsBackToAlternateModel(t *testing.T) {
	var models []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		models = append(models, body["model"].(string))
		if body["model"] == "glm-5.1" {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"temporary failure"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"turbo\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c, err := New(provider.Config{
		Name: "router", Kind: Kind, DefaultModel: "glm-5.1",
		Extra: map[string]any{
			"_providers": map[string]provider.Config{
				"zai": {Name: "zai", Kind: "openai-chat", BaseURL: srv.URL, APIKey: "k"},
			},
			"routes": []any{map[string]any{
				"match":        "glm-5.1",
				"provider":     "zai",
				"target_model": "glm-5.1",
				"fallbacks": []any{map[string]any{
					"provider":     "zai",
					"target_model": "glm-5-turbo",
				}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chunks, errs := c.StreamChat(context.Background(), []provider.Message{{Role: "user", Content: "hi"}}, provider.StreamOptions{Model: "glm-5.1"})
	text := ""
	for ch := range chunks {
		text += ch.Text
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if text != "turbo" || fmt.Sprint(models) != "[glm-5.1 glm-5-turbo]" {
		t.Fatalf("text=%q models=%v", text, models)
	}
}

func TestRouterAliasesAndProviderWildcardModels(t *testing.T) {
	c, err := New(provider.Config{
		Name: "router", Kind: Kind,
		Extra: map[string]any{
			"_providers": map[string]provider.Config{
				"ollama-cloud": {Name: "ollama-cloud", Kind: "openai-chat", BaseURL: "http://127.0.0.1", APIKey: "k"},
			},
			"aliases": map[string]any{"oss": "ollama/gpt-oss:120b"},
			"routes": []any{map[string]any{
				"models":       []any{"ollama/*"},
				"provider":     "ollama-cloud",
				"target_model": "$1",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chain, ok := c.resolveChain(c.expandAlias("oss"))
	if !ok || len(chain) != 1 {
		t.Fatalf("chain=%+v ok=%v", chain, ok)
	}
	cfg, target, _, ok := c.targetConfig(chain[0].index, chain[0].route, "ollama/gpt-oss:120b")
	if !ok || cfg.Name != "ollama-cloud" || target != "gpt-oss:120b" {
		t.Fatalf("cfg=%+v target=%q ok=%v", cfg, target, ok)
	}
}

func TestRouterRenamesExposedModelNames(t *testing.T) {
	c, err := New(provider.Config{
		Name: "router", Kind: Kind,
		Extra: map[string]any{
			"_providers": map[string]provider.Config{
				"ollama-cloud": {
					Name: "ollama-cloud", Kind: "openai-chat", BaseURL: "http://127.0.0.1", APIKey: "k",
					Models: []provider.ModelInfo{{ModelID: "gpt-oss:120b", Name: "GPT-OSS 120B"}},
				},
			},
			"routes": []any{map[string]any{
				"models":            []any{"*"},
				"provider":          "ollama-cloud",
				"target_model":      "$model",
				"model_name_rename": "ollama:{name}",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	models := c.AvailableModels()
	if len(models) != 1 || models[0].ModelID != "ollama:gpt-oss:120b" {
		t.Fatalf("models = %+v", models)
	}
	chain, ok := c.resolveChain("ollama:gpt-oss:120b")
	if !ok || len(chain) != 1 {
		t.Fatalf("chain=%+v ok=%v", chain, ok)
	}
	cfg, target, _, ok := c.targetConfig(chain[0].index, chain[0].route, "ollama:gpt-oss:120b")
	if !ok || cfg.Name != "ollama-cloud" || target != "gpt-oss:120b" {
		t.Fatalf("cfg=%+v target=%q ok=%v", cfg, target, ok)
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
	if got := resolveTargetModel(r, "zai:glm-5.1", "zai:glm-5.1", "fallback"); got != "glm-5.1" {
		t.Fatalf("target = %q", got)
	}
}
