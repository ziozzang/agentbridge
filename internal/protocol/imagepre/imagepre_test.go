package imagepre

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/ziozzang/agentbridge/internal/acp"
)

type fakeVision struct {
	called  bool
	gotArgs map[string]any
	out     string
	err     error
}

func (f *fakeVision) CallTool(_ context.Context, _ string, args map[string]any) (string, error) {
	f.called = true
	f.gotArgs = args
	return f.out, f.err
}

func TestNoImagePassesThrough(t *testing.T) {
	in := []acp.ContentBlock{{Type: "text", Text: "hello"}}
	res := Preprocess(context.Background(), in, nil)
	if len(res.Blocks) != 1 || res.Blocks[0].Text != "hello" {
		t.Errorf("unexpected: %+v", res.Blocks)
	}
}

func TestImageWithoutVisionAnnotated(t *testing.T) {
	in := []acp.ContentBlock{
		{Type: "image", MimeType: "image/png", URI: "https://x/y.png"},
	}
	res := Preprocess(context.Background(), in, nil)
	if !strings.Contains(res.Blocks[0].Text, "image_attached") {
		t.Errorf("missing annotation: %+v", res.Blocks)
	}
}

func TestImageWithVisionURI(t *testing.T) {
	v := &fakeVision{out: "a cat"}
	in := []acp.ContentBlock{
		{Type: "image", MimeType: "image/png", URI: "https://x/y.png"},
	}
	res := Preprocess(context.Background(), in, v)
	if !v.called || v.gotArgs["image_source"] != "https://x/y.png" {
		t.Errorf("vision not called correctly: %+v", v.gotArgs)
	}
	if !strings.Contains(res.Blocks[0].Text, "a cat") {
		t.Errorf("wrong analysis: %s", res.Blocks[0].Text)
	}
}

func TestImageWithVisionBase64Materialized(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
	v := &fakeVision{out: "diagram"}
	in := []acp.ContentBlock{
		{Type: "image", MimeType: "image/png", Data: data},
	}
	res := Preprocess(context.Background(), in, v)
	defer func() {
		for _, c := range res.Cleanups {
			c()
		}
	}()
	if !v.called {
		t.Fatal("vision not called")
	}
	src, _ := v.gotArgs["image_source"].(string)
	if src == "" || !strings.HasSuffix(src, ".png") {
		t.Errorf("source path not png: %q", src)
	}
	if len(res.Cleanups) != 1 {
		t.Errorf("expected one cleanup, got %d", len(res.Cleanups))
	}
}

func TestImageMissingDataAndURI(t *testing.T) {
	v := &fakeVision{}
	in := []acp.ContentBlock{{Type: "image", MimeType: "image/png"}}
	res := Preprocess(context.Background(), in, v)
	if !strings.Contains(res.Blocks[0].Text, "image_analysis_error") {
		t.Errorf("expected error annotation: %s", res.Blocks[0].Text)
	}
}

func TestImageVisionFailureDegradedToError(t *testing.T) {
	v := &fakeVision{err: errors.New("boom")}
	in := []acp.ContentBlock{{Type: "image", MimeType: "image/png", URI: "https://x"}}
	res := Preprocess(context.Background(), in, v)
	if !strings.Contains(res.Blocks[0].Text, "image_analysis_error") || !strings.Contains(res.Blocks[0].Text, "boom") {
		t.Errorf("expected degraded error: %s", res.Blocks[0].Text)
	}
}

func TestRenderToString(t *testing.T) {
	blocks := []acp.ContentBlock{
		{Type: "text", Text: "hello"},
		{Type: "resource_link", Name: "main.go", URI: "file:///tmp/main.go"},
		{Type: "resource", Resource: &acp.EmbeddedResource{URI: "x", Text: "body"}},
	}
	got := RenderToString(blocks)
	if !strings.Contains(got, "hello") || !strings.Contains(got, "[main.go](file:///tmp/main.go)") || !strings.Contains(got, "<resource") {
		t.Errorf("got %q", got)
	}
}
