// Package plugins defines the harness's extension surface. A Plugin adds
// new tool definitions (function-call schemas) that the agent advertises to
// the model and routes invocations to.
//
// Plugins are activated by listing them in AGENTBRIDGE_PLUGINS, comma-
// separated. ACP_HARNESS_PLUGINS remains a supported alias. The activation
// is order-preserving but case-insensitive.
//
// Concrete plugins live in subpackages (e.g. plugins/sqlite) and register
// themselves at init time.
package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/ziozzang/agentbridge/internal/logger"
	"github.com/ziozzang/agentbridge/internal/tools/definitions"
)

// ToolDef is a plugin-contributed tool schema.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Plugin is the harness extension contract.
type Plugin interface {
	// Name is the short identifier the user passes in
	// AGENTBRIDGE_PLUGINS (e.g. "sqlite").
	Name() string
	// Tools returns the function-call schemas this plugin contributes.
	Tools() []ToolDef
	// Call dispatches an invocation. tool is the plugin tool name (no
	// `plugin__` prefix); args is the raw JSON arguments object.
	Call(ctx context.Context, tool string, args json.RawMessage) (string, error)
}

var (
	registryMu sync.RWMutex
	registry   = map[string]func() Plugin{}
)

// Register adds a plugin constructor under name. Re-registering replaces
// the previous constructor.
func Register(name string, ctor func() Plugin) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[strings.ToLower(name)] = ctor
}

// Available returns the sorted list of registered plugin names.
func Available() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Active is the set of plugins instantiated for the current run, in the
// order requested by the user.
type Active struct {
	plugins []Plugin
	byName  map[string]Plugin
}

// LoadActive reads AGENTBRIDGE_PLUGINS and instantiates each requested
// plugin. Unknown names are logged as a warning and skipped so a typo does
// not bring the agent down.
func LoadActive() *Active {
	spec := envFirst("AGENTBRIDGE_PLUGINS", "ACP_HARNESS_PLUGINS")
	a := &Active{byName: map[string]Plugin{}}
	if spec == "" {
		return a
	}
	registryMu.RLock()
	defer registryMu.RUnlock()
	for _, raw := range strings.Split(spec, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		ctor, ok := registry[name]
		if !ok {
			logger.Warnf("plugin %q is not registered; available: %v", name, plainNames())
			continue
		}
		p := ctor()
		a.plugins = append(a.plugins, p)
		a.byName[name] = p
		logger.Infof("plugin %q activated", name)
	}
	return a
}

func envFirst(names ...string) string {
	for _, name := range names {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

// plainNames is registryNames() but expected to be called while holding
// registryMu (RLock).
func plainNames() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Plugins exposes the active plugins in order.
func (a *Active) Plugins() []Plugin {
	out := make([]Plugin, len(a.plugins))
	copy(out, a.plugins)
	return out
}

// ToolNamePrefix is the "plugin__<name>__<tool>" prefix used to identify
// plugin tools in the function-call surface.
const ToolNamePrefix = "plugin__"

// ToolName builds the harness-side tool name for a plugin tool.
func ToolName(plugin, tool string) string {
	return ToolNamePrefix + plugin + "__" + tool
}

// SplitToolName reverses ToolName.
func SplitToolName(name string) (plugin, tool string, ok bool) {
	if !strings.HasPrefix(name, ToolNamePrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(name, ToolNamePrefix)
	i := strings.Index(rest, "__")
	if i < 0 {
		return "", "", false
	}
	return rest[:i], rest[i+2:], true
}

// Tools returns the union of all active plugin tool definitions
// transformed into the harness-wide definitions.Tool shape.
func (a *Active) Tools() []definitions.Tool {
	var out []definitions.Tool
	for _, p := range a.plugins {
		for _, td := range p.Tools() {
			out = append(out, definitions.Tool{
				Type: "function",
				Function: definitions.ToolFunction{
					Name:        ToolName(p.Name(), td.Name),
					Description: td.Description,
					Parameters:  td.Parameters,
				},
			})
		}
	}
	return out
}

// Dispatch routes a tool invocation to the right plugin. It returns
// (result, true, err) when the call belongs to a plugin, and
// ("", false, nil) when it should fall through to the built-in executor.
func (a *Active) Dispatch(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	plugin, tool, ok := SplitToolName(name)
	if !ok {
		return "", false, nil
	}
	p, ok := a.byName[plugin]
	if !ok {
		return "", true, fmt.Errorf("plugins: %q is not active (active: %v)", plugin, a.ActiveNames())
	}
	if args == nil || len(args) == 0 {
		args = json.RawMessage("{}")
	}
	out, err := p.Call(ctx, tool, args)
	return out, true, err
}

// ActiveNames returns the names of currently active plugins, in load order.
func (a *Active) ActiveNames() []string {
	out := make([]string, 0, len(a.plugins))
	for _, p := range a.plugins {
		out = append(out, p.Name())
	}
	return out
}
