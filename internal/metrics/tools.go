package metrics

import (
	"fmt"
	"strings"
	"sync"
)

var toolCalls = &toolRegistry{counts: map[string]uint64{}}

type toolRegistry struct {
	mu     sync.Mutex
	counts map[string]uint64
}

var generations = &generationRegistry{
	requests: map[string]uint64{},
}

type generationRegistry struct {
	mu                   sync.Mutex
	requests             map[string]uint64
	firstTokenLatencySum timeDuration
	generationSecondsSum timeDuration
	tokensTotal          uint64
	tpsSum               float64
	tpsCount             uint64
}

type timeDuration float64

func ObserveToolCall(kind, name string, ok bool) {
	toolCalls.mu.Lock()
	defer toolCalls.mu.Unlock()
	status := "error"
	if ok {
		status = "ok"
	}
	key := fmt.Sprintf("%s|%s|%s", kind, sanitize(name), status)
	toolCalls.counts[key]++
}

func ObserveGeneration(model string, firstTokenSeconds, generationSeconds float64, outputTokens int, ok bool) {
	generations.mu.Lock()
	defer generations.mu.Unlock()
	status := "error"
	if ok {
		status = "ok"
	}
	key := fmt.Sprintf("%s|%s", sanitize(firstNonEmpty(model, "unknown")), status)
	generations.requests[key]++
	if firstTokenSeconds > 0 {
		generations.firstTokenLatencySum += timeDuration(firstTokenSeconds)
	}
	if generationSeconds > 0 {
		generations.generationSecondsSum += timeDuration(generationSeconds)
	}
	if outputTokens > 0 {
		generations.tokensTotal += uint64(outputTokens)
		if generationSeconds > 0 {
			generations.tpsSum += float64(outputTokens) / generationSeconds
			generations.tpsCount++
		}
	}
}

func Prometheus() string {
	toolCalls.mu.Lock()
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP agentbridge_tool_calls_total Tool calls by kind, name, and status.\n")
	fmt.Fprintf(&b, "# TYPE agentbridge_tool_calls_total counter\n")
	for key, count := range toolCalls.counts {
		parts := strings.SplitN(key, "|", 3)
		if len(parts) == 3 {
			fmt.Fprintf(&b, "agentbridge_tool_calls_total{kind=%q,name=%q,status=%q} %d\n", parts[0], parts[1], parts[2], count)
		}
	}
	toolCalls.mu.Unlock()
	generations.mu.Lock()
	fmt.Fprintf(&b, "# HELP agentbridge_llm_requests_total Provider generation requests by model and status.\n")
	fmt.Fprintf(&b, "# TYPE agentbridge_llm_requests_total counter\n")
	for key, count := range generations.requests {
		parts := strings.SplitN(key, "|", 2)
		if len(parts) == 2 {
			fmt.Fprintf(&b, "agentbridge_llm_requests_total{model=%q,status=%q} %d\n", parts[0], parts[1], count)
		}
	}
	fmt.Fprintf(&b, "# HELP agentbridge_llm_first_token_latency_seconds_sum Sum of first non-empty token latency.\n")
	fmt.Fprintf(&b, "# TYPE agentbridge_llm_first_token_latency_seconds_sum counter\n")
	fmt.Fprintf(&b, "agentbridge_llm_first_token_latency_seconds_sum %.6f\n", float64(generations.firstTokenLatencySum))
	fmt.Fprintf(&b, "# HELP agentbridge_llm_generation_duration_seconds_sum Sum of token generation durations after first token.\n")
	fmt.Fprintf(&b, "# TYPE agentbridge_llm_generation_duration_seconds_sum counter\n")
	fmt.Fprintf(&b, "agentbridge_llm_generation_duration_seconds_sum %.6f\n", float64(generations.generationSecondsSum))
	fmt.Fprintf(&b, "# HELP agentbridge_llm_output_tokens_total Output tokens observed from usage or estimated from text.\n")
	fmt.Fprintf(&b, "# TYPE agentbridge_llm_output_tokens_total counter\n")
	fmt.Fprintf(&b, "agentbridge_llm_output_tokens_total %d\n", generations.tokensTotal)
	fmt.Fprintf(&b, "# HELP agentbridge_llm_tokens_per_second_sum Sum of per-request output token rates.\n")
	fmt.Fprintf(&b, "# TYPE agentbridge_llm_tokens_per_second_sum counter\n")
	fmt.Fprintf(&b, "agentbridge_llm_tokens_per_second_sum %.6f\n", generations.tpsSum)
	fmt.Fprintf(&b, "# HELP agentbridge_llm_tokens_per_second_count Number of requests with a TPS sample.\n")
	fmt.Fprintf(&b, "# TYPE agentbridge_llm_tokens_per_second_count counter\n")
	fmt.Fprintf(&b, "agentbridge_llm_tokens_per_second_count %d\n", generations.tpsCount)
	generations.mu.Unlock()
	return b.String()
}

func sanitize(v string) string {
	if len(v) > 120 {
		return v[:120]
	}
	return v
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
