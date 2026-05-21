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

	"github.com/ziozzang/agentbridge/internal/tools/definitions"
)

// ErrNativeCompactionUnavailable tells callers to fall back to their generic
// compaction strategy without treating the provider as unhealthy.
var ErrNativeCompactionUnavailable = errors.New("provider-native compaction is not available")

// ModelInfo describes a model advertised to ACP clients.
type ModelInfo struct {
	ModelID       string         `json:"modelId"`
	Name          string         `json:"name"`
	Description   string         `json:"description,omitempty"`
	Provider      string         `json:"provider,omitempty"`
	API           string         `json:"api,omitempty"`
	BaseURL       string         `json:"baseUrl,omitempty"`
	Input         []string       `json:"input,omitempty"`
	Reasoning     *bool          `json:"reasoning,omitempty"`
	ContextWindow int            `json:"contextWindow,omitempty"`
	ContextTokens int            `json:"contextTokens,omitempty"`
	MaxTokens     int            `json:"maxTokens,omitempty"`
	Status        string         `json:"status,omitempty"`
	StatusReason  string         `json:"statusReason,omitempty"`
	Replaces      []string       `json:"replaces,omitempty"`
	ReplacedBy    string         `json:"replacedBy,omitempty"`
	Aliases       []string       `json:"aliases,omitempty"`
	Tags          []string       `json:"tags,omitempty"`
	Compat        map[string]any `json:"compat,omitempty"`
	Cost          map[string]any `json:"cost,omitempty"`
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
	Type             string        `json:"type,omitempty"`
	Role             string        `json:"role"`
	Content          any           `json:"content,omitempty"`
	Name             string        `json:"name,omitempty"`
	ToolCalls        []ToolCallMsg `json:"tool_calls,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
	EncryptedContent string        `json:"encrypted_content,omitempty"`
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

// IntentionChoice is one candidate in an experimental logprob-based
// classification probe.
type IntentionChoice struct {
	Label string `json:"label"`
	Text  string `json:"text,omitempty"`
}

// IntentionProbeRequest asks a provider to choose among short labeled choices
// and return normalized confidence from token log probabilities.
type IntentionProbeRequest struct {
	Model       string            `json:"model,omitempty"`
	Prompt      string            `json:"prompt,omitempty"`
	Messages    []Message         `json:"messages,omitempty"`
	Choices     []IntentionChoice `json:"choices"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	TopLogprobs int               `json:"top_logprobs,omitempty"`
}

// TokenLogprob describes a token candidate returned by an upstream logprob
// API. Logprob is natural-log probability.
type TokenLogprob struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
}

// IntentionProbeResult is returned by providers that can expose token-level
// log probabilities for short classification probes.
type IntentionProbeResult struct {
	Model      string             `json:"model,omitempty"`
	Provider   string             `json:"provider,omitempty"`
	Answer     string             `json:"answer"`
	Index      int                `json:"index"`
	Confidence float64            `json:"confidence"`
	Logprobs   map[string]float64 `json:"logprobs"`
	Text       string             `json:"text,omitempty"`
	Tokens     []TokenLogprob     `json:"tokens,omitempty"`
}

// IntentionProber is experimental. Implementations should only advertise
// support when the upstream actually returns usable top token logprobs.
type IntentionProber interface {
	ProbeIntention(ctx context.Context, req IntentionProbeRequest) (IntentionProbeResult, error)
}

// StreamOptions tunes a single StreamChat invocation.
type StreamOptions struct {
	Model                string
	Tools                []definitions.Tool
	SessionID            string
	PromptCacheKey       string
	PromptCacheRetention string
	ServiceTier          string
	ReasoningEffort      string
	ReasoningSummary     string
}

// CompactOptions tunes provider-native conversation compaction.
type CompactOptions struct {
	Model                string
	Tools                []definitions.Tool
	TargetTokens         int
	Reason               string
	SessionID            string
	PromptCacheKey       string
	PromptCacheRetention string
	ServiceTier          string
	ReasoningEffort      string
	ReasoningSummary     string
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

// ConversationCompactor is implemented by providers with a native compaction
// endpoint, such as Codex's /responses/compact API.
type ConversationCompactor interface {
	CompactConversation(ctx context.Context, messages []Message, opts CompactOptions) ([]Message, error)
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
