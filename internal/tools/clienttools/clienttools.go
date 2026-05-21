// Package clienttools defines the ACP client-owned tool namespace.
package clienttools

import (
	"encoding/json"
	"strings"

	"github.com/ziozzang/agentbridge/internal/tools/definitions"
)

const Prefix = "client__"

// AdvertisedTool is a tool definition supplied by an ACP client.
type AdvertisedTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolDefinitions converts client-local tool names to server-visible tool
// names under the client__ namespace.
func ToolDefinitions(tools []AdvertisedTool) []definitions.Tool {
	out := make([]definitions.Tool, 0, len(tools))
	for _, t := range tools {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, definitions.Tool{
			Type: "function",
			Function: definitions.ToolFunction{
				Name:        Prefix + sanitizeName(name),
				Description: firstNonEmpty(t.Description, "Client-provided tool: "+name),
				Parameters:  params,
			},
		})
	}
	return out
}

// LocalName strips the server-visible namespace.
func LocalName(name string) (string, bool) {
	if !strings.HasPrefix(name, Prefix) {
		return "", false
	}
	return strings.TrimPrefix(name, Prefix), true
}

func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
