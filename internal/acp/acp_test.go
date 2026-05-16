package acp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// pipeAgent is a minimal Agent stub used by transport tests.
type pipeAgent struct {
	mu              sync.Mutex
	initCalls       int
	promptCalls     int
	cancelCalls     int
	gotPromptParams PromptParams
	notify          chan struct{} // signalled at end of Prompt to allow assertions

	pendingForCall func(ctx context.Context, c *Conn) // hook executed inside a method
}

func (p *pipeAgent) Initialize(_ context.Context, _ InitializeParams) (InitializeResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.initCalls++
	return InitializeResponse{
		ProtocolVersion: 1,
		AgentInfo:       AgentInfo{Name: "test", Version: "0"},
	}, nil
}
func (p *pipeAgent) Authenticate(context.Context, json.RawMessage) (any, error) {
	return map[string]any{}, nil
}
func (p *pipeAgent) NewSession(context.Context, NewSessionParams) (NewSessionResponse, error) {
	return NewSessionResponse{SessionID: "s1"}, nil
}
func (p *pipeAgent) LoadSession(context.Context, LoadSessionParams) (LoadSessionResponse, error) {
	return LoadSessionResponse{}, nil
}
func (p *pipeAgent) ForkSession(context.Context, LoadSessionParams) (ForkSessionResponse, error) {
	return ForkSessionResponse{SessionID: "fork"}, nil
}
func (p *pipeAgent) ResumeSession(context.Context, LoadSessionParams) (LoadSessionResponse, error) {
	return LoadSessionResponse{}, nil
}
func (p *pipeAgent) Prompt(ctx context.Context, in PromptParams) (PromptResponse, error) {
	p.mu.Lock()
	p.promptCalls++
	p.gotPromptParams = in
	hook := p.pendingForCall
	p.mu.Unlock()
	if hook != nil {
		hook(ctx, nil)
	}
	if p.notify != nil {
		p.notify <- struct{}{}
	}
	return PromptResponse{StopReason: "end_turn"}, nil
}
func (p *pipeAgent) Cancel(_ context.Context, _ CancelParams) {
	p.mu.Lock()
	p.cancelCalls++
	p.mu.Unlock()
}
func (p *pipeAgent) CloseSession(context.Context, CloseSessionParams) (any, error) {
	return map[string]any{}, nil
}
func (p *pipeAgent) ListSessions(context.Context, ListSessionsParams) (ListSessionsResponse, error) {
	return ListSessionsResponse{}, nil
}
func (p *pipeAgent) SetSessionMode(context.Context, SetModeParams) (any, error) {
	return map[string]any{}, nil
}
func (p *pipeAgent) SetSessionModel(context.Context, SetModelParams) (any, error) {
	return map[string]any{}, nil
}

// makePipeConn returns a connection plus the writer/reader to drive it.
func makePipeConn(t *testing.T, agent Agent) (*Conn, io.WriteCloser, *concurrentBuilder) {
	pr, pw := io.Pipe()
	out := &concurrentBuilder{}
	conn := NewConn(pr, out, agent)
	go func() {
		_ = conn.Run()
	}()
	t.Cleanup(func() {
		pw.Close()
		<-conn.Done()
	})
	return conn, pw, out
}

type concurrentBuilder struct {
	mu sync.Mutex
	b  *strings.Builder
}

func (c *concurrentBuilder) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.b == nil {
		c.b = &strings.Builder{}
	}
	return c.b.Write(p)
}

func (c *concurrentBuilder) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.b == nil {
		return ""
	}
	return c.b.String()
}

func (c *concurrentBuilder) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.b == nil {
		return 0
	}
	return c.b.Len()
}

func TestRequestResponseRoundTrip(t *testing.T) {
	agent := &pipeAgent{}
	_, pw, out := makePipeConn(t, agent)
	_ = out
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n"
	if _, err := pw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		agent.mu.Lock()
		n := agent.initCalls
		agent.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	agent.mu.Lock()
	gotInit := agent.initCalls
	agent.mu.Unlock()
	if gotInit != 1 {
		t.Fatalf("init not called, got %d", gotInit)
	}
	// Wait for the response to land in `out`.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if out.Len() > 0 && strings.Contains(out.String(), `"agentInfo"`) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(out.String(), `"agentInfo":{"name":"test"`) {
		t.Errorf("response body wrong: %q", out.String())
	}
}

func TestNotificationDispatch(t *testing.T) {
	agent := &pipeAgent{}
	_, pw, _ := makePipeConn(t, agent)
	body := `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s1"}}` + "\n"
	pw.Write([]byte(body))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		agent.mu.Lock()
		n := agent.cancelCalls
		agent.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("cancel not dispatched")
}

func TestUnknownMethodReturnsError(t *testing.T) {
	agent := &pipeAgent{}
	_, pw, out := makePipeConn(t, agent)
	pw.Write([]byte(`{"jsonrpc":"2.0","id":7,"method":"bogus","params":{}}` + "\n"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), "method not found") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("expected error response, got %q", out.String())
}
