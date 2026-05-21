package llamacpp

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

func (c *Client) ProbeIntention(ctx context.Context, req provider.IntentionProbeRequest) (provider.IntentionProbeResult, error) {
	choices := normalizeProbeChoices(req.Choices)
	if len(choices) == 0 {
		return provider.IntentionProbeResult{}, errors.New("intention probe requires at least one choice")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" && len(req.Messages) > 0 {
		prompt = messagesPrompt(req.Messages)
	}
	prompt = buildProbePrompt(prompt, choices)
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 8
	}
	topLogprobs := req.TopLogprobs
	if topLogprobs <= 0 {
		topLogprobs = min(len(choices)+3, 20)
	}
	bodyMap := map[string]any{
		"prompt":      prompt,
		"max_tokens":  maxTokens,
		"temperature": 0,
		"logprobs":    topLogprobs,
	}
	if model := c.upstreamModel(req.Model); model != "" {
		bodyMap["model"] = model
	}
	body, err := json.Marshal(bodyMap)
	if err != nil {
		return provider.IntentionProbeResult{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.BaseURL, "/")+"/v1/completions", bytes.NewReader(body))
	if err != nil {
		return provider.IntentionProbeResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	c.setAuth(httpReq)
	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return provider.IntentionProbeResult{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return provider.IntentionProbeResult{}, fmt.Errorf("%s HTTP %d: %s", c.Name(), resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out completionLogprobResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return provider.IntentionProbeResult{}, err
	}
	if len(out.Choices) == 0 || len(out.Choices[0].Logprobs.Content) == 0 {
		return provider.IntentionProbeResult{}, errors.New("upstream did not return completion logprobs")
	}
	result, err := scoreCompletionTokens(choices, out.Choices[0])
	if err != nil {
		return provider.IntentionProbeResult{}, err
	}
	result.Model = firstNonEmpty(out.Model, req.Model, c.DefaultModel())
	result.Provider = c.Name()
	return result, nil
}

type completionLogprobResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Text     string `json:"text"`
		Logprobs struct {
			Content []completionToken `json:"content"`
		} `json:"logprobs"`
	} `json:"choices"`
}

type completionToken struct {
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

func messagesPrompt(messages []provider.Message) string {
	var parts []string
	for _, msg := range messages {
		if s, ok := msg.Content.(string); ok && strings.TrimSpace(s) != "" {
			parts = append(parts, strings.ToUpper(firstNonEmpty(msg.Role, "user"))+": "+s)
		}
	}
	return strings.Join(parts, "\n")
}

func scoreCompletionTokens(choices []provider.IntentionChoice, choice struct {
	Text     string `json:"text"`
	Logprobs struct {
		Content []completionToken `json:"content"`
	} `json:"logprobs"`
}) (provider.IntentionProbeResult, error) {
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
		if _, ok := labels[token]; ok {
			scores[token] += math.Exp(top.Logprob)
		}
	}
	if len(scores) == 0 {
		token := strings.TrimSpace(first.Token)
		if _, ok := labels[token]; ok {
			scores[token] = math.Exp(first.Logprob)
			tokens = append(tokens, provider.TokenLogprob{Token: first.Token, Logprob: first.Logprob})
		}
	}
	if len(scores) == 0 {
		return provider.IntentionProbeResult{}, errors.New("no choice labels found in first-token logprobs")
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
	return provider.IntentionProbeResult{Answer: bestLabel, Index: labels[bestLabel], Confidence: bestScore, Logprobs: normalized, Text: choice.Text, Tokens: tokens}, nil
}
