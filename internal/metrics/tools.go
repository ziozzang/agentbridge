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

func Prometheus() string {
	toolCalls.mu.Lock()
	defer toolCalls.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP agentbridge_tool_calls_total Tool calls by kind, name, and status.\n")
	fmt.Fprintf(&b, "# TYPE agentbridge_tool_calls_total counter\n")
	for key, count := range toolCalls.counts {
		parts := strings.SplitN(key, "|", 3)
		if len(parts) == 3 {
			fmt.Fprintf(&b, "agentbridge_tool_calls_total{kind=%q,name=%q,status=%q} %d\n", parts[0], parts[1], parts[2], count)
		}
	}
	return b.String()
}

func sanitize(v string) string {
	if len(v) > 120 {
		return v[:120]
	}
	return v
}
