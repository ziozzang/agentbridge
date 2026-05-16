package imagepre

import (
	"context"
	"strings"
	"testing"

	"github.com/ziozzang/glm-acp/internal/acp"
)

// TS test: "preprocessImageBlocks prefers base64 data over URI when both are present".
func TestImagePrefersBase64OverURIWhenBothPresent(t *testing.T) {
	v := &fakeVision{out: "analysed"}
	in := []acp.ContentBlock{{
		Type:     "image",
		MimeType: "image/png",
		Data:     "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
		URI:      "https://example.com/should-not-be-used.png",
	}}
	res := Preprocess(context.Background(), in, v)
	defer func() {
		for _, fn := range res.Cleanups {
			fn()
		}
	}()
	src, _ := v.gotArgs["image_source"].(string)
	if src == "https://example.com/should-not-be-used.png" {
		t.Fatalf("URI was used despite base64 being present")
	}
	if src == "" || !strings.Contains(src, "glm-acp-image-") {
		t.Errorf("expected local materialised path, got %q", src)
	}
	if len(res.Cleanups) != 1 {
		t.Errorf("expected 1 cleanup, got %d", len(res.Cleanups))
	}
}
