package imagepre

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/ziozzang/agentbridge/internal/acp"
)

// BuildPromptBlockDiagnosticLines returns a list of safe, redacted strings
// describing the inbound ACP prompt blocks. Intended for debug logging only:
// the caller must guard with logger.IsDebug() before calling so the string
// work is skipped in production.
//
// Safety rules (mirror src/protocol/image-preprocessor.ts):
//   - Image blocks: log MIME, URI presence, and approximate decoded byte
//     length. Never log the base64 payload.
//   - Resource / resource_link blocks: log only the URL scheme and the
//     basename of the path — no full paths, no query strings, no data:
//     payloads.
func BuildPromptBlockDiagnosticLines(blocks []acp.ContentBlock) []string {
	counts := map[string]int{}
	for _, b := range blocks {
		counts[b.Type]++
	}
	// Build "type×N" summary in alphabetical order so output is stable.
	types := make([]string, 0, len(counts))
	for t := range counts {
		types = append(types, t)
	}
	// Insertion sort (small slices); avoids importing "sort" for this single use.
	for i := 1; i < len(types); i++ {
		for j := i; j > 0 && types[j-1] > types[j]; j-- {
			types[j-1], types[j] = types[j], types[j-1]
		}
	}
	parts := make([]string, len(types))
	for i, t := range types {
		parts[i] = fmt.Sprintf("%s×%d", t, counts[t])
	}
	lines := []string{"prompt blocks: " + strings.Join(parts, ", ")}

	for _, b := range blocks {
		switch b.Type {
		case "image":
			hasURI := b.URI != ""
			// Approximate decoded length from base64 length so we don't
			// overstate raw payload size.
			dataBytes := 0
			if b.Data != "" {
				dataBytes = (len(b.Data) * 3) / 4
			}
			lines = append(lines, fmt.Sprintf(
				"  image block: mime=%s uri=%t data_bytes≈%d",
				b.MimeType, hasURI, dataBytes))
		case "resource_link":
			mimePart := ""
			if b.MimeType != "" {
				mimePart = " mime=" + b.MimeType
			}
			lines = append(lines, fmt.Sprintf(
				"  resource_link block: name=%s uri=%s%s",
				b.Name, safeURISummary(b.URI), mimePart))
		case "resource":
			if b.Resource == nil {
				continue
			}
			mimePart := ""
			if b.Resource.MimeType != "" {
				mimePart = " mime=" + b.Resource.MimeType
			}
			lines = append(lines, fmt.Sprintf(
				"  resource block: uri=%s%s",
				safeURISummary(b.Resource.URI), mimePart))
		}
	}
	return lines
}

// safeURISummary returns a redacted, single-line summary of an arbitrary URI.
// data: URLs are fully redacted; URLs are reduced to scheme + basename;
// anything that won't parse is fallen back to "…/basename".
func safeURISummary(raw string) string {
	if strings.HasPrefix(raw, "data:") {
		return "data:<redacted>"
	}
	normalized := strings.ReplaceAll(raw, "\\", "/")
	if u, err := url.Parse(normalized); err == nil && u.Scheme != "" {
		base := ""
		for _, seg := range strings.Split(u.Path, "/") {
			if seg != "" {
				base = seg
			}
		}
		if base != "" {
			return u.Scheme + "://..." + "/" + base
		}
		return u.Scheme + "://..."
	}
	base := normalized
	for _, seg := range strings.Split(normalized, "/") {
		if seg != "" {
			base = seg
		}
	}
	return ".../" + base
}
