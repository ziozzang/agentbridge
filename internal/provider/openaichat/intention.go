package openaichat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/ziozzang/agentbridge/internal/provider"
)

// ProbeIntention implements the experimental logprob-based classifier over
// non-streaming Chat Completions. It is intentionally narrow: the upstream
// must return choices[0].logprobs.content[*].top_logprobs.
func (c *Client) ProbeIntention(ctx context.Context, req provider.IntentionProbeRequest) (provider.IntentionProbeResult, error) {
	model := firstNonEmpty(req.Model, c.DefaultModel())
	choices := normalizeProbeChoices(req.Choices)
	if len(choices) == 0 {
		return provider.IntentionProbeResult{}, errors.New("intention probe requires at least one choice")
	}
	messages := req.Messages
	if len(messages) == 0 {
		messages = []provider.Message{{Role: "user", Content: buildProbePrompt(req.Prompt, choices)}}
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16
	}
	topLogprobs := req.TopLogprobs
	if topLogprobs <= 0 {
		topLogprobs = min(len(choices)+3, 20)
	}

	bodyMap := map[string]any{
		"model":        model,
		"messages":     messages,
		"max_tokens":   maxTokens,
		"temperature":  0,
		"logprobs":     true,
		"top_logprobs": topLogprobs,
	}
	for k, v := range asMap(c.cfg.Extra["request_defaults"]) {
		if strings.TrimSpace(k) != "" {
			bodyMap[k] = v
		}
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return provider.IntentionProbeResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return provider.IntentionProbeResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if c.cfg.APIKey != "" {
		httpReq.Header.Set(c.cfg.AuthHeader, c.cfg.AuthPrefix+c.cfg.APIKey)
	}
	for k, v := range c.cfg.Headers {
		httpReq.Header.Set(k, v)
	}
	for k, v := range c.openRouterResponseCacheHeaders() {
		httpReq.Header.Set(k, v)
	}

	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return provider.IntentionProbeResult{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return provider.IntentionProbeResult{}, parseAPIError(c.Name(), model, resp.StatusCode, raw)
	}

	var out chatLogprobResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return provider.IntentionProbeResult{}, err
	}
	if len(out.Choices) == 0 || len(out.Choices[0].Logprobs.Content) == 0 {
		return provider.IntentionProbeResult{}, errors.New("upstream did not return chat logprobs")
	}
	result, err := scoreProbeTokens(choices, out.Choices[0])
	if err != nil {
		return provider.IntentionProbeResult{}, err
	}
	result.Model = firstNonEmpty(out.Model, model)
	result.Provider = c.Name()
	return result, nil
}

type chatLogprobResponse struct {
	Model   string              `json:"model"`
	Choices []chatLogprobChoice `json:"choices"`
}

type chatLogprobChoice struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Logprobs struct {
		Content []chatLogprobToken `json:"content"`
	} `json:"logprobs"`
}

type chatLogprobToken struct {
	Token       string  `json:"token"`
	Logprob     float64 `json:"logprob"`
	TopLogprobs []struct {
		Token   string  `json:"token"`
		Logprob float64 `json:"logprob"`
	} `json:"top_logprobs"`
}

func normalizeProbeChoices(in []provider.IntentionChoice) []provider.IntentionChoice {
	out := make([]provider.IntentionChoice, 0, len(in))
	for i, ch := range in {
		label := strings.TrimSpace(ch.Label)
		if label == "" && i < 26 {
			label = string(rune('A' + i))
		}
		if label == "" {
			continue
		}
		ch.Label = label
		out = append(out, ch)
	}
	return out
}

func buildProbePrompt(prompt string, choices []provider.IntentionChoice) string {
	var b strings.Builder
	if strings.TrimSpace(prompt) != "" {
		b.WriteString(strings.TrimSpace(prompt))
		b.WriteString("\n\n")
	}
	b.WriteString("Choose exactly one label. Return only the label.\n")
	for _, ch := range choices {
		b.WriteString(ch.Label)
		if ch.Text != "" {
			b.WriteString(": ")
			b.WriteString(ch.Text)
		}
		b.WriteByte('\n')
	}
	b.WriteString("Answer:")
	return b.String()
}

func scoreProbeTokens(choices []provider.IntentionChoice, choice chatLogprobChoice) (provider.IntentionProbeResult, error) {
	labels := map[string]int{}
	for i, ch := range choices {
		labels[ch.Label] = i
	}
	first := choice.Logprobs.Content[0]
	scores := map[string]float64{}
	tokens := make([]provider.TokenLogprob, 0, len(first.TopLogprobs))
	for _, top := range first.TopLogprobs {
		token := strings.TrimSpace(top.Token)
		tokens = append(tokens, provider.TokenLogprob{Token: top.Token, Logprob: top.Logprob})
		if _, ok := labels[token]; !ok {
			continue
		}
		scores[token] += math.Exp(top.Logprob)
	}
	if len(scores) == 0 {
		token := strings.TrimSpace(first.Token)
		if _, ok := labels[token]; ok {
			scores[token] = math.Exp(first.Logprob)
			tokens = append(tokens, provider.TokenLogprob{Token: first.Token, Logprob: first.Logprob})
		}
	}
	if len(scores) == 0 {
		return provider.IntentionProbeResult{}, fmt.Errorf("no choice labels found in first-token logprobs")
	}
	total := 0.0
	for _, v := range scores {
		total += v
	}
	bestLabel := ""
	bestScore := -1.0
	normalized := map[string]float64{}
	for _, ch := range choices {
		v := scores[ch.Label]
		if total > 0 {
			v = v / total
		}
		normalized[ch.Label] = v
		if v > bestScore {
			bestScore = v
			bestLabel = ch.Label
		}
	}
	return provider.IntentionProbeResult{
		Answer:     bestLabel,
		Index:      labels[bestLabel],
		Confidence: bestScore,
		Logprobs:   normalized,
		Text:       choice.Message.Content,
		Tokens:     tokens,
	}, nil
}
