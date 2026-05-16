package mcparg

import (
	"reflect"
	"strings"
	"testing"
)

func TestRemapArguments_PassThroughWhenNoSchema(t *testing.T) {
	in := map[string]any{"query": "hello", "count": 5}
	got := RemapArguments(in, nil)
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("expected pass-through, got %v", got)
	}
}

func TestRemapArguments_ExactMatchPassesThrough(t *testing.T) {
	got := RemapArguments(map[string]any{"query": "hi"}, []string{"query", "count"})
	if got["query"] != "hi" {
		t.Fatalf("expected query=hi, got %v", got)
	}
	if _, ok := got["search_query"]; ok {
		t.Fatalf("did not expect search_query alias when query is a real prop")
	}
}

func TestRemapArguments_AliasRemap(t *testing.T) {
	got := RemapArguments(map[string]any{"query": "hi"}, []string{"search_query", "count"})
	if got["search_query"] != "hi" {
		t.Fatalf("expected query→search_query, got %v", got)
	}
	if _, ok := got["query"]; ok {
		t.Fatalf("did not expect raw query key after remap")
	}
}

func TestRemapArguments_UnknownKeyPassesThrough(t *testing.T) {
	got := RemapArguments(map[string]any{"foo": "bar"}, []string{"search_query"})
	if got["foo"] != "bar" {
		t.Fatalf("expected unknown key passthrough, got %v", got)
	}
}

func TestResolveToolName_NoToolsPassesThrough(t *testing.T) {
	got, err := ResolveToolName("anything", nil, "ctx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "anything" {
		t.Fatalf("expected anything, got %q", got)
	}
}

func TestResolveToolName_ExactMatch(t *testing.T) {
	got, err := ResolveToolName("webSearchPrime", []string{"foo", "webSearchPrime"}, "ctx")
	if err != nil || got != "webSearchPrime" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestResolveToolName_KeywordFallback(t *testing.T) {
	got, err := ResolveToolName("web_search", []string{"webSearchPrime"}, "ctx")
	if err != nil || got != "webSearchPrime" {
		t.Fatalf("expected search keyword fallback to webSearchPrime, got %q err=%v", got, err)
	}
}

func TestResolveToolName_KeywordFallbackImage(t *testing.T) {
	got, err := ResolveToolName("image_recognition", []string{"image_analysis"}, "ctx")
	if err != nil || got != "image_analysis" {
		t.Fatalf("expected image keyword fallback, got %q err=%v", got, err)
	}
}

func TestResolveToolName_NoMatchError(t *testing.T) {
	_, err := ResolveToolName("totally_unknown", []string{"webReader", "image_analysis"}, "ctx@server")
	if err == nil {
		t.Fatalf("expected an error")
	}
	if !strings.Contains(err.Error(), "ctx@server") {
		t.Fatalf("expected error to mention context, got %v", err)
	}
	if !strings.Contains(err.Error(), "webReader") {
		t.Fatalf("expected error to list available tools, got %v", err)
	}
}
