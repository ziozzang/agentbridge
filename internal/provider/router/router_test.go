package router

import (
	"context"
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
