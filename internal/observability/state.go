package observability

import (
	"sort"
	"sync"
	"time"
)

type ProviderState struct {
	Name        string `json:"name,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Model       string `json:"model,omitempty"`
	BaseURL     string `json:"base_url,omitempty"`
	NativeAgent bool   `json:"native_agent,omitempty"`
}

type HTTPRequestState struct {
	ID         string `json:"id"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	StartedAt  string `json:"started_at"`
	DurationMS int64  `json:"duration_ms"`
}

type SessionState struct {
	SessionID    string `json:"session_id"`
	Cwd          string `json:"cwd,omitempty"`
	Model        string `json:"model,omitempty"`
	Mode         string `json:"mode,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
	NativeAgent  bool   `json:"native_agent,omitempty"`
	MessageCount int    `json:"message_count,omitempty"`
}

type Snapshot struct {
	Now            string             `json:"now"`
	ProcessStart   string             `json:"process_start"`
	Provider       ProviderState      `json:"provider"`
	ActiveRequests []HTTPRequestState `json:"active_requests"`
	ActiveSessions []SessionState     `json:"active_sessions"`
	CompletedHTTP  uint64             `json:"completed_http_requests"`
	FailedHTTP     uint64             `json:"failed_http_requests"`
}

var global = &registry{
	start:    time.Now(),
	requests: map[string]HTTPRequestState{},
	sessions: map[string]SessionState{},
}

type registry struct {
	mu            sync.Mutex
	start         time.Time
	provider      ProviderState
	requests      map[string]HTTPRequestState
	sessions      map[string]SessionState
	completedHTTP uint64
	failedHTTP    uint64
}

func SetProvider(state ProviderState) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.provider = state
}

func BeginHTTPRequest(id, method, path string) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.requests[id] = HTTPRequestState{
		ID:        id,
		Method:    method,
		Path:      path,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

func EndHTTPRequest(id string, status int) {
	global.mu.Lock()
	defer global.mu.Unlock()
	req, ok := global.requests[id]
	if ok {
		if started, err := time.Parse(time.RFC3339, req.StartedAt); err == nil {
			req.DurationMS = time.Since(started).Milliseconds()
		}
		delete(global.requests, id)
	}
	global.completedHTTP++
	if status >= 500 {
		global.failedHTTP++
	}
}

func UpsertSession(state SessionState) {
	global.mu.Lock()
	defer global.mu.Unlock()
	global.sessions[state.SessionID] = state
}

func DeleteSession(sessionID string) {
	global.mu.Lock()
	defer global.mu.Unlock()
	delete(global.sessions, sessionID)
}

func SnapshotState() Snapshot {
	global.mu.Lock()
	defer global.mu.Unlock()
	requests := make([]HTTPRequestState, 0, len(global.requests))
	for _, req := range global.requests {
		if started, err := time.Parse(time.RFC3339, req.StartedAt); err == nil {
			req.DurationMS = time.Since(started).Milliseconds()
		}
		requests = append(requests, req)
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].StartedAt < requests[j].StartedAt })
	sessions := make([]SessionState, 0, len(global.sessions))
	for _, session := range global.sessions {
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].UpdatedAt > sessions[j].UpdatedAt })
	return Snapshot{
		Now:            time.Now().UTC().Format(time.RFC3339),
		ProcessStart:   global.start.UTC().Format(time.RFC3339),
		Provider:       global.provider,
		ActiveRequests: requests,
		ActiveSessions: sessions,
		CompletedHTTP:  global.completedHTTP,
		FailedHTTP:     global.failedHTTP,
	}
}
