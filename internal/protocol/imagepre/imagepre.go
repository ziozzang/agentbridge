// Package imagepre annotates ACP image blocks with text that the GLM model
// can read. When a Vision MCP client is configured we materialize the image
// and ask it for a description; otherwise we attach a simple
// `<image_attached>` annotation so the prompt still has context.
package imagepre

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ziozzang/glm-acp/internal/acp"
)

// VisionClient is the minimal interface expected from a vision provider.
// The Go port currently has no built-in vision implementation, but the
// surface is here so a future MCP/HTTP integration can plug in.
type VisionClient interface {
	CallTool(ctx context.Context, name string, args map[string]any) (string, error)
}

// Result captures the preprocessed prompt blocks plus per-image cleanup
// callbacks the caller must invoke after sending the message.
type Result struct {
	Blocks   []acp.ContentBlock
	Cleanups []func()
}

// Preprocess walks the prompt blocks, replacing image blocks with text
// annotations describing the image (or noting why we couldn't analyze it).
func Preprocess(ctx context.Context, blocks []acp.ContentBlock, vc VisionClient) Result {
	hasImage := false
	for _, b := range blocks {
		if b.Type == "image" {
			hasImage = true
			break
		}
	}
	if !hasImage {
		dup := make([]acp.ContentBlock, len(blocks))
		copy(dup, blocks)
		return Result{Blocks: dup}
	}

	out := make([]acp.ContentBlock, 0, len(blocks))
	cleanups := []func(){}
	imageIndex := 0

	for _, b := range blocks {
		if b.Type != "image" {
			out = append(out, b)
			continue
		}
		imageIndex++
		if vc == nil {
			out = append(out, textBlock(fmt.Sprintf(
				`<image_attached index="%d" mime="%s">image attached (not analyzed; Vision MCP unavailable)</image_attached>`,
				imageIndex, b.MimeType)))
			continue
		}
		var imageSource string
		if b.Data != "" {
			path, cleanup, err := materializeImage(b.Data, b.MimeType, imageIndex)
			if err != nil {
				out = append(out, textBlock(fmt.Sprintf(
					`<image_analysis_error index="%d">%s</image_analysis_error>`, imageIndex, err.Error())))
				continue
			}
			imageSource = path
			cleanups = append(cleanups, cleanup)
		} else if b.URI != "" {
			imageSource = b.URI
		} else {
			out = append(out, textBlock(fmt.Sprintf(
				`<image_analysis_error index="%d">image block has neither a uri nor base64 data</image_analysis_error>`, imageIndex)))
			continue
		}
		text, err := vc.CallTool(ctx, "image_analysis", map[string]any{
			"image_source": imageSource,
			"prompt":       "Describe this image in detail, including any text, code, UI elements, or other visible content.",
		})
		if err != nil {
			out = append(out, textBlock(fmt.Sprintf(
				`<image_analysis_error index="%d">%s</image_analysis_error>`, imageIndex, err.Error())))
			continue
		}
		out = append(out, textBlock(fmt.Sprintf(
			"<image_analysis index=\"%d\">\n%s\n</image_analysis>", imageIndex, text)))
	}
	return Result{Blocks: out, Cleanups: cleanups}
}

// RenderToString flattens preprocessed blocks into the plain-string user
// message we send to the chat-completions endpoint.
func RenderToString(blocks []acp.ContentBlock) string {
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "resource_link":
			parts = append(parts, fmt.Sprintf("[%s](%s)", b.Name, b.URI))
		case "resource":
			if b.Resource != nil && b.Resource.Text != "" {
				parts = append(parts, fmt.Sprintf("<resource uri=%q>\n%s\n</resource>", b.Resource.URI, b.Resource.Text))
			} else if b.Resource != nil && b.Resource.Blob != "" {
				parts = append(parts, fmt.Sprintf("[binary resource](%s)", b.Resource.URI))
			}
		case "image":
			parts = append(parts, fmt.Sprintf("[image: %s]", b.MimeType))
		case "audio":
			parts = append(parts, "[unsupported audio block]")
		default:
			parts = append(parts, fmt.Sprintf("[unknown block type %s]", b.Type))
		}
	}
	return strings.Join(parts, "\n")
}

func textBlock(text string) acp.ContentBlock {
	return acp.ContentBlock{Type: "text", Text: text}
}

func materializeImage(data, mime string, idx int) (string, func(), error) {
	dir, err := os.MkdirTemp("", "glm-acp-image-")
	if err != nil {
		return "", nil, err
	}
	ext := guessExt(mime)
	path := filepath.Join(dir, fmt.Sprintf("image-%d%s", idx, ext))
	bytes, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("decode base64: %w", err)
	}
	if err := os.WriteFile(path, bytes, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	return path, func() { _ = os.RemoveAll(dir) }, nil
}

func guessExt(mime string) string {
	switch strings.ToLower(mime) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}
