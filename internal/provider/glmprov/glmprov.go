// Package glmprov registers the "glm" provider kind, which is a thin
// preset over the OpenAI Chat Completions provider with Z.AI / Zhipu AI
// defaults — GLM "thinking" extension on by default, Coding Plan base URL,
// and the curated GLM-5 model list.
//
// Users can also reach GLM with `kind: openai-chat` and the same base URL;
// this kind exists primarily for clarity and back-compatibility.
package glmprov

import (
	"context"
	"regexp"

	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/provider/openaichat"
)

// Kind is the registry key for the GLM provider preset.
const Kind = "glm"

// DefaultBaseURL is Z.AI's Coding Plan endpoint.
const DefaultBaseURL = "https://api.z.ai/api/coding/paas/v4"

// DefaultModels mirrors the curated TypeScript list.
var DefaultModels = []provider.ModelInfo{
	{ModelID: "glm-5.1", Name: "GLM-5.1", Description: "Latest GLM reasoning model with thinking mode"},
	{ModelID: "glm-5-turbo", Name: "GLM-5 Turbo", Description: "Faster Coding Plan reasoning model"},
	{ModelID: "glm-4.7", Name: "GLM-4.7", Description: "200K-context reasoning model"},
	{ModelID: "glm-4.5-air", Name: "GLM-4.5 Air", Description: "Lightweight, lower-latency model"},
}

// thinkingPattern identifies models that benefit from GLM's reasoning
// extension. It mirrors the TS implementation.
var thinkingPattern = regexp.MustCompile(`(?i)^glm-(?:4\.[567]|5)`)

func init() {
	provider.Register(Kind, func(cfg provider.Config) (provider.Provider, error) {
		if cfg.BaseURL == "" {
			cfg.BaseURL = DefaultBaseURL
		}
		if len(cfg.Models) == 0 {
			cfg.Models = append([]provider.ModelInfo(nil), DefaultModels...)
		}
		if cfg.DefaultModel == "" {
			cfg.DefaultModel = "glm-5.1"
		}
		if cfg.Thinking == "" && thinkingPattern.MatchString(cfg.DefaultModel) {
			cfg.Thinking = "enabled"
		}
		base := openaichat.New(cfg)
		return &thinkingProvider{base: base}, nil
	})
}

// thinkingProvider toggles the GLM `thinking` flag on a per-request basis
// according to the selected model.
type thinkingProvider struct {
	base *openaichat.Client
}

func (p *thinkingProvider) Name() string                          { return "glm" }
func (p *thinkingProvider) Kind() string                          { return Kind }
func (p *thinkingProvider) AvailableModels() []provider.ModelInfo { return p.base.AvailableModels() }
func (p *thinkingProvider) DefaultModel() string                  { return p.base.DefaultModel() }
func (p *thinkingProvider) ContextWindow(model string) int        { return p.base.ContextWindow(model) }

func (p *thinkingProvider) StreamChat(ctx context.Context, msgs []provider.Message, opts provider.StreamOptions) (<-chan provider.Chunk, <-chan error) {
	// Per-request toggle: clone the underlying config so concurrent
	// streams using different models don't race on the Thinking field.
	cfg := p.base.Config()
	model := opts.Model
	if model == "" {
		model = cfg.DefaultModel
	}
	if thinkingPattern.MatchString(model) {
		cfg.Thinking = "enabled"
	} else {
		cfg.Thinking = ""
	}
	transient := openaichat.New(cfg)
	return transient.StreamChat(ctx, msgs, opts)
}
