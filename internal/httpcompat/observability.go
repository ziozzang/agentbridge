package httpcompat

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ziozzang/agentbridge/internal/logger"
	"github.com/ziozzang/agentbridge/internal/metrics"
)

var httpMetrics = &metricsRegistry{
	start:  time.Now(),
	counts: map[string]uint64{},
}

type metricsRegistry struct {
	mu        sync.Mutex
	start     time.Time
	requests  uint64
	failures  uint64
	inflight  uint64
	durations time.Duration
	counts    map[string]uint64
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	return n, err
}

func (w *statusRecorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (h *handler) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		httpMetrics.begin()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		httpMetrics.finish(r.URL.Path, status, time.Since(start))
		logger.Infof("http request method=%s path=%s status=%d bytes=%d duration_ms=%d", r.Method, r.URL.Path, status, rec.bytes, time.Since(start).Milliseconds())
	})
}

func (h *handler) metrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprint(w, httpMetrics.prometheus())
}

func (m *metricsRegistry) begin() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests++
	m.inflight++
}

func (m *metricsRegistry) finish(path string, status int, dur time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inflight > 0 {
		m.inflight--
	}
	if status >= 500 {
		m.failures++
	}
	m.durations += dur
	key := fmt.Sprintf("%s|%d", routeLabel(path), status)
	m.counts[key]++
}

func (m *metricsRegistry) prometheus() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP agentbridge_http_requests_total Total HTTP requests.\n")
	fmt.Fprintf(&b, "# TYPE agentbridge_http_requests_total counter\n")
	fmt.Fprintf(&b, "agentbridge_http_requests_total %d\n", m.requests)
	fmt.Fprintf(&b, "# HELP agentbridge_http_failures_total Total HTTP 5xx responses.\n")
	fmt.Fprintf(&b, "# TYPE agentbridge_http_failures_total counter\n")
	fmt.Fprintf(&b, "agentbridge_http_failures_total %d\n", m.failures)
	fmt.Fprintf(&b, "# HELP agentbridge_http_inflight_requests Current HTTP requests in flight.\n")
	fmt.Fprintf(&b, "# TYPE agentbridge_http_inflight_requests gauge\n")
	fmt.Fprintf(&b, "agentbridge_http_inflight_requests %d\n", m.inflight)
	fmt.Fprintf(&b, "# HELP agentbridge_http_request_duration_seconds_sum Total HTTP request duration.\n")
	fmt.Fprintf(&b, "# TYPE agentbridge_http_request_duration_seconds_sum counter\n")
	fmt.Fprintf(&b, "agentbridge_http_request_duration_seconds_sum %.6f\n", m.durations.Seconds())
	fmt.Fprintf(&b, "# HELP agentbridge_http_route_responses_total HTTP responses by route and status.\n")
	fmt.Fprintf(&b, "# TYPE agentbridge_http_route_responses_total counter\n")
	for key, count := range m.counts {
		parts := strings.SplitN(key, "|", 2)
		fmt.Fprintf(&b, "agentbridge_http_route_responses_total{route=%q,status=%q} %d\n", parts[0], parts[1], count)
	}
	b.WriteString(metrics.Prometheus())
	fmt.Fprintf(&b, "# HELP agentbridge_process_start_time_seconds Process start time.\n")
	fmt.Fprintf(&b, "# TYPE agentbridge_process_start_time_seconds gauge\n")
	fmt.Fprintf(&b, "agentbridge_process_start_time_seconds %d\n", m.start.Unix())
	return b.String()
}

func routeLabel(path string) string {
	switch {
	case strings.HasPrefix(path, "/v1/responses/"), strings.HasPrefix(path, "/responses/"):
		return "/v1/responses/{id}"
	case strings.HasPrefix(path, "/v1/"):
		return path
	case strings.HasPrefix(path, "/"):
		return path
	default:
		return "unknown"
	}
}
