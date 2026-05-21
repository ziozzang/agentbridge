package pipeline

import (
	"context"
	"errors"
	"regexp"

	"github.com/ziozzang/agentbridge/internal/pii"
	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/runtimeconfig"
	"github.com/ziozzang/agentbridge/internal/sanitize"
)

// Wrap attaches protocol-agnostic safety behavior to a provider. It returns
// the original provider when no runtime safety option is enabled.
func Wrap(p provider.Provider, cfg runtimeconfig.Config) provider.Provider {
	if p == nil || (!cfg.PII.Enabled && !cfg.Sanitize.StripThinkTags) {
		return p
	}
	w := &Wrapper{inner: p, cfg: cfg}
	if cfg.PII.Enabled {
		w.detector = buildDetector(cfg.PII)
		w.mask = true
		if cfg.PII.Mask != nil {
			w.mask = *cfg.PII.Mask
		}
	}
	if cfg.Sanitize.StripThinkTags {
		w.stripTags = cfg.Sanitize.Tags
		w.strip = sanitize.Compile(cfg.Sanitize.Tags)
	}
	return w
}

// WrapFromConfig loads runtime config and wraps p. Config load failures leave
// p unchanged so provider construction does not fail because of optional
// safety settings.
func WrapFromConfig(p provider.Provider) provider.Provider {
	cfg, err := runtimeconfig.Load()
	if err != nil {
		return p
	}
	return Wrap(p, cfg)
}

type Wrapper struct {
	inner     provider.Provider
	cfg       runtimeconfig.Config
	detector  *pii.Detector
	mask      bool
	strip     []*regexp.Regexp
	stripTags []string
}

func (w *Wrapper) Name() string                          { return w.inner.Name() }
func (w *Wrapper) Kind() string                          { return w.inner.Kind() }
func (w *Wrapper) AvailableModels() []provider.ModelInfo { return w.inner.AvailableModels() }
func (w *Wrapper) DefaultModel() string                  { return w.inner.DefaultModel() }
func (w *Wrapper) ContextWindow(model string) int        { return w.inner.ContextWindow(model) }

func (w *Wrapper) StreamChat(ctx context.Context, messages []provider.Message, opts provider.StreamOptions) (<-chan provider.Chunk, <-chan error) {
	messages, mapping, _, err := w.prepare(messages, &opts.Model)
	if err != nil {
		chunks := make(chan provider.Chunk)
		errs := make(chan error, 1)
		close(chunks)
		errs <- err
		close(errs)
		return chunks, errs
	}
	upstreamChunks, upstreamErrs := w.inner.StreamChat(ctx, messages, opts)
	return w.wrapChunks(upstreamChunks, upstreamErrs, mapping)
}

func (w *Wrapper) CompactConversation(ctx context.Context, messages []provider.Message, opts provider.CompactOptions) ([]provider.Message, error) {
	compactor, ok := w.inner.(provider.ConversationCompactor)
	if !ok {
		return nil, provider.ErrNativeCompactionUnavailable
	}
	messages, mapping, _, err := w.prepare(messages, &opts.Model)
	if err != nil {
		return nil, err
	}
	out, err := compactor.CompactConversation(ctx, messages, opts)
	if err != nil {
		return nil, err
	}
	return pii.UnmaskMessages(out, mapping), nil
}

func (w *Wrapper) ProbeIntention(ctx context.Context, req provider.IntentionProbeRequest) (provider.IntentionProbeResult, error) {
	prober, ok := w.inner.(provider.IntentionProber)
	if !ok {
		return provider.IntentionProbeResult{}, errors.New("active provider does not support experimental intention probe")
	}
	messages, mapping, _, err := w.prepare(req.Messages, &req.Model)
	if err != nil {
		return provider.IntentionProbeResult{}, err
	}
	req.Messages = messages
	result, err := prober.ProbeIntention(ctx, req)
	if err != nil {
		return provider.IntentionProbeResult{}, err
	}
	result.Text = pii.Unmask(result.Text, mapping)
	return result, nil
}

func (w *Wrapper) prepare(messages []provider.Message, model *string) ([]provider.Message, pii.Mapping, bool, error) {
	if w.detector == nil {
		return messages, nil, false, nil
	}
	masked, mapping, detected := pii.MaskMessages(w.detector, messages, w.mask)
	if !detected {
		return messages, nil, false, nil
	}
	if w.cfg.PII.Routing.Reject {
		msg := w.cfg.PII.Routing.RejectMessage
		if msg == "" {
			msg = "PII detected"
		}
		return nil, mapping, true, errors.New(msg)
	}
	if w.cfg.PII.Routing.RouteTo != "" && model != nil {
		*model = w.cfg.PII.Routing.RouteTo
	}
	return masked, mapping, true, nil
}

func (w *Wrapper) wrapChunks(upstreamChunks <-chan provider.Chunk, upstreamErrs <-chan error, mapping pii.Mapping) (<-chan provider.Chunk, <-chan error) {
	out := make(chan provider.Chunk, 32)
	errs := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errs)
		var textUnmasker *pii.StreamUnmasker
		var thinkingUnmasker *pii.StreamUnmasker
		if len(mapping) > 0 {
			textUnmasker = pii.NewStreamUnmasker(mapping, w.detector.MaxTokenLen())
			thinkingUnmasker = pii.NewStreamUnmasker(mapping, w.detector.MaxTokenLen())
		}
		textStripper, thinkingStripper := w.newStrippers()
		var pendingDone *provider.Chunk
		for ch := range upstreamChunks {
			ch = w.processChunk(ch, mapping, textUnmasker, thinkingUnmasker, textStripper, thinkingStripper)
			if ch.Done {
				cp := ch
				pendingDone = &cp
				continue
			}
			if ch.Text == "" && ch.Thinking == "" && ch.ToolCall == nil && ch.Usage == nil && ch.StopReason == "" {
				continue
			}
			out <- ch
		}
		if tail := flushText(textUnmasker, textStripper); tail != "" {
			out <- provider.Chunk{Text: tail}
		}
		if tail := flushText(thinkingUnmasker, thinkingStripper); tail != "" {
			out <- provider.Chunk{Thinking: tail}
		}
		if pendingDone != nil {
			out <- *pendingDone
		}
		if err := <-upstreamErrs; err != nil {
			errs <- err
			return
		}
		errs <- nil
	}()
	return out, errs
}

func (w *Wrapper) processChunk(ch provider.Chunk, mapping pii.Mapping, textUnmasker, thinkingUnmasker *pii.StreamUnmasker, textStripper, thinkingStripper *sanitize.StreamStripper) provider.Chunk {
	if textUnmasker != nil && ch.Text != "" {
		ch.Text = textUnmasker.Write(ch.Text)
	}
	if thinkingUnmasker != nil && ch.Thinking != "" {
		ch.Thinking = thinkingUnmasker.Write(ch.Thinking)
	}
	if ch.Text != "" && textStripper != nil {
		ch.Text = textStripper.Write(ch.Text)
	}
	if ch.Thinking != "" && thinkingStripper != nil {
		ch.Thinking = thinkingStripper.Write(ch.Thinking)
	}
	if ch.ToolCall != nil {
		ch = pii.UnmaskChunk(ch, mapping)
	}
	return ch
}

func (w *Wrapper) newStrippers() (*sanitize.StreamStripper, *sanitize.StreamStripper) {
	if w.strip == nil {
		return nil, nil
	}
	return sanitize.NewStreamStripper(w.strip, w.stripTags), sanitize.NewStreamStripper(w.strip, w.stripTags)
}

func flushText(unmasker *pii.StreamUnmasker, stripper *sanitize.StreamStripper) string {
	out := ""
	if unmasker != nil {
		out = unmasker.Flush()
	}
	if stripper != nil {
		if out != "" {
			out = stripper.Write(out)
		}
		out += stripper.Flush()
	}
	return out
}

func buildDetector(cfg runtimeconfig.PII) *pii.Detector {
	var d *pii.Detector
	if cfg.DisableDefaults {
		d = pii.NewEmpty()
	} else {
		d = pii.New()
	}
	for _, p := range cfg.Patterns {
		_ = d.Add(pii.Pattern{Name: p.Name, Regex: p.Regex, Mask: p.Mask})
	}
	if cfg.Env.File != "" {
		mask := cfg.Env.Mask
		if mask == "" {
			mask = "[MASK_ENV_SECRET_{n}]"
		}
		if secrets, err := pii.LoadEnvSecrets(cfg.Env.File, cfg.Env.Names, cfg.Env.MinLength); err == nil {
			for _, secret := range secrets {
				_ = d.AddLiteral("env_"+secret.Name, secret.Value, mask)
			}
		}
	}
	return d
}
