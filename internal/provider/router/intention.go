package router

import (
	"context"
	"errors"
	"fmt"

	"github.com/ziozzang/agentbridge/internal/provider"
)

// ProbeIntention routes the experimental intention probe to the same backend
// selected for normal chat traffic, if that backend exposes logprobs.
func (c *Client) ProbeIntention(ctx context.Context, req provider.IntentionProbeRequest) (provider.IntentionProbeResult, error) {
	model := req.Model
	if model == "" {
		model = c.DefaultModel()
	}
	model = c.expandAlias(model)
	chain, ok := c.resolveChain(model)
	if !ok {
		return provider.IntentionProbeResult{}, fmt.Errorf("router: no route for model %q", model)
	}
	var lastErr error
	for _, candidate := range chain {
		cfg, target, keySig, ok := c.targetConfig(candidate.index, candidate.route, model)
		if !ok {
			lastErr = fmt.Errorf("router: route provider %q is not configured", candidate.route.Provider)
			continue
		}
		if err := resolveOAuthConfig(&cfg); err != nil {
			return provider.IntentionProbeResult{}, err
		}
		release, err := c.acquirePermit(ctx, candidate.index, candidate.route, keySig)
		if err != nil {
			return provider.IntentionProbeResult{}, err
		}
		p, err := provider.Build(cfg)
		if err != nil {
			release()
			return provider.IntentionProbeResult{}, err
		}
		prober, ok := p.(provider.IntentionProber)
		if !ok {
			release()
			lastErr = fmt.Errorf("router: provider %q does not support intention probe", cfg.Name)
			continue
		}
		call := req
		call.Model = target
		result, err := prober.ProbeIntention(ctx, call)
		release()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !routeShouldFallback(err) {
			return provider.IntentionProbeResult{}, err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("router: no fallback route succeeded")
	}
	return provider.IntentionProbeResult{}, lastErr
}
