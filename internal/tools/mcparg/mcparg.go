// Package mcparg implements argument and tool-name remapping for upstream MCP
// servers whose schemas don't exactly match the names our agent issues.
//
// Mirrors src/tools/mcp-arg-remap.ts in the TypeScript reference. Two
// behaviours are exposed:
//
//  1. RemapArguments — translate agent-side argument keys to upstream property
//     names using an alias table, falling back to a pass-through when no
//     schema is available.
//  2. ResolveToolName — pick a real upstream tool name given a requested name
//     and the list of tools the server advertised via tools/list. Exact match
//     wins; otherwise a keyword-based fallback picks a sensible substitute
//     (e.g. searches for "search" inside the tool list). When no match is
//     found, a descriptive error is returned.
package mcparg

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ziozzang/agentbridge/internal/logger"
)

// DiscoveredTool is a tool exposed by an upstream MCP server along with the
// keys declared in its input schema's `properties` block.
type DiscoveredTool struct {
	Name       string
	Properties []string
}

// argAliases maps agent-side argument names to their upstream MCP property
// equivalents. Kept tiny and explicit — adding aliases for ad-hoc cases is
// preferred over fuzzy matching.
var argAliases = map[string]string{
	"query": "search_query",
}

// RemapArguments returns a copy of requestedArgs with keys translated to
// match targetProperties.
//
//   - If a key already matches a target property, it is passed through unchanged.
//   - Otherwise the alias table is consulted; if the alias exists in the
//     target properties the key is remapped.
//   - If no schema is available (empty targetProperties), every argument is
//     passed through unchanged.
func RemapArguments(requestedArgs map[string]any, targetProperties []string) map[string]any {
	if len(targetProperties) == 0 {
		out := make(map[string]any, len(requestedArgs))
		for k, v := range requestedArgs {
			out[k] = v
		}
		return out
	}
	known := make(map[string]struct{}, len(targetProperties))
	for _, p := range targetProperties {
		known[p] = struct{}{}
	}
	out := make(map[string]any, len(requestedArgs))
	// Iterate keys in deterministic order so debug logs are stable.
	keys := make([]string, 0, len(requestedArgs))
	for k := range requestedArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := requestedArgs[key]
		if _, ok := known[key]; ok {
			out[key] = value
			continue
		}
		alias, hasAlias := argAliases[key]
		if hasAlias {
			if _, ok := known[alias]; ok {
				logger.Debugf("mcparg: remapped arg %q → %q", key, alias)
				out[alias] = value
				continue
			}
		}
		out[key] = value
	}
	return out
}

// ResolveToolName resolves requestedName against the names the upstream
// server advertises.
//
//   - Exact match takes priority.
//   - Falls back to keyword-based search (e.g. "search", "reader", "image").
//   - If availableTools is empty (discovery not available), requestedName is
//     returned unchanged.
//   - If no match can be found, a descriptive error is returned.
func ResolveToolName(requestedName string, availableTools []string, context string) (string, error) {
	if len(availableTools) == 0 {
		return requestedName, nil
	}
	for _, n := range availableTools {
		if n == requestedName {
			return n, nil
		}
	}
	for _, keyword := range extractToolKeywords(requestedName) {
		for _, candidate := range availableTools {
			if strings.Contains(strings.ToLower(candidate), keyword) {
				logger.Debugf("mcparg: resolved tool name %q → %q via keyword %q",
					requestedName, candidate, keyword)
				return candidate, nil
			}
		}
	}
	return "", fmt.Errorf("Tool %q not available on %s. Available tools: [%s]",
		requestedName, context, strings.Join(availableTools, ", "))
}

func extractToolKeywords(name string) []string {
	lower := strings.ToLower(name)
	var keywords []string
	if strings.Contains(lower, "search") {
		keywords = append(keywords, "search")
	}
	if strings.Contains(lower, "reader") {
		keywords = append(keywords, "reader")
	}
	if strings.Contains(lower, "image") ||
		strings.Contains(lower, "vision") ||
		strings.Contains(lower, "analysis") ||
		strings.Contains(lower, "recognition") {
		keywords = append(keywords, "image", "vision", "analysis", "recognition")
	}
	return keywords
}
