// Package provider defines the generic provider abstraction that backs the
// ACP harness. Each concrete provider (GLM, OpenAI Chat Completions, OpenAI
// Responses, Anthropic Messages, Ollama, …) implements this interface and
// translates between the harness's neutral message/chunk types and the
// upstream API.
//
// Provider implementations live in sibling packages under
// internal/provider/<kind>. They are registered with this package's registry
// so the harness can instantiate them by kind name (e.g. "openai-chat",
// "anthropic", "glm").
package provider

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/ziozzang/glm-acp/internal/tools/definitions"
)

// ModelInfo describes a model advertised to ACP clients.
type ModelInfo struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ToolCall is an assembled function call streamed from the model.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCallMsg is the message-shape of a recorded tool call.
type ToolCallMsg struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function ToolCallMsgFunction `json:"function"`
}

// ToolCallMsgFunction is the function payload for ToolCallMsg.
type ToolCallMsgFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Message is the neutral chat message format used between the agent and
// every provider adapter. It is intentionally OpenAI-Chat-shaped because
// that is the most ubiquitous on-the-wire format; adapters translate to
// other formats (Anthropic, Responses, Ollama) as needed.
type Message struct {
	Role       string        `json:"role"`
	Content    any           `json:"content,omitempty"`
	Name       string        `json:"name,omitempty"`
	ToolCalls  []ToolCallMsg `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

// Usage mirrors the ACP token-usage shape.
type Usage struct {
	InputTokens      int `json:"inputTokens"`
	OutputTokens     int `json:"outputTokens"`
	TotalTokens      int `json:"totalTokens"`
	CachedReadTokens int `json:"cachedReadTokens,omitempty"`
	ThoughtTokens    int `json:"thoughtTokens,omitempty"`
}

// Chunk is a single streaming event yielded by a provider.
type Chunk struct {
	Text       string
	Thinking   string
	ToolCall   *ToolCall
	Usage      *Usage
	Done       bool
	StopReason string
}

// StreamOptions tunes a single StreamChat invocation.
type StreamOptions struct {
	Model string
	Tools []definitions.Tool
}

// Provider is the harness-side abstraction every adapter implements.
type Provider interface {
	Name() string
	Kind() string
	AvailableModels() []ModelInfo
	DefaultModel() string
	ContextWindow(model string) int
	StreamChat(ctx context.Context, messages []Message, opts StreamOptions) (<-chan Chunk, <-chan error)
}

// Config carries the user-provided / template-derived knobs every adapter
// needs. Concrete adapters embed this and add their own fields.
type Config struct {
	Name          string
	Kind          string
	BaseURL       string
	APIKey        string
	AuthHeader    string
	AuthPrefix    string
	Headers       map[string]string
	Models        []ModelInfo
	DefaultModel  string
	MaxTokens     int
	ContextWindow int
	Thinking      string
	Extra         map[string]any
}

// Factory builds a concrete Provider from a Config.
type Factory func(cfg Config) (Provider, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]Factory{}
)

// Register adds a factory for a provider kind. Re-registering a kind
// overwrites the previous factory (useful in tests).
func Register(kind string, f Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[kind] = f
}

// Build instantiates the provider matching cfg.Kind.
func Build(cfg Config) (Provider, error) {
	if cfg.Kind == "" {
		return nil, errors.New("provider: empty kind")
	}
	registryMu.RLock()
	f, ok := registry[cfg.Kind]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("provider: unknown kind %q (registered: %v)", cfg.Kind, RegisteredKinds())
	}
	return f(cfg)
}

// RegisteredKinds returns the sorted list of kinds the registry knows.
func RegisteredKinds() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	kinds := make([]string, 0, len(registry))
	for k := range registry {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// ContextOverflowError is the typed error adapters return when the upstream
// API rejects a request because the supplied messages exceed the model's
// context window.
type ContextOverflowError struct {
	Provider string
	Model    string
	Message  string
	Cause    error
}

func (e *ContextOverflowError) Error() string {
	if e == nil {
		return ""
	}
	if e.Provider == "" {
		return fmt.Sprintf("context overflow: %s", e.Message)
	}
	return fmt.Sprintf("context overflow (%s/%s): %s", e.Provider, e.Model, e.Message)
}

func (e *ContextOverflowError) Unwrap() error { return e.Cause }

// IsContextOverflow reports whether err is (or wraps) a ContextOverflowError.
func IsContextOverflow(err error) bool {
	var coe *ContextOverflowError
	return errors.As(err, &coe)
}
