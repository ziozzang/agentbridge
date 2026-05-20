package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRunSetupWritesKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	in := strings.NewReader("super-key-9999\n")
	var out bytes.Buffer
	if err := runSetup(in, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Saved.") {
		t.Errorf("expected 'Saved.' in: %q", out.String())
	}
}

func TestRunSetupRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	in := strings.NewReader("\n")
	var out bytes.Buffer
	if err := runSetup(in, &out); err == nil {
		t.Error("expected error for empty key")
	}
}

func TestServeListenerHandlesACPConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- serveListener(ctx, ln, 1) }()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n"
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion int `json:"protocolVersion"`
			AgentInfo       struct {
				Name string `json:"name"`
			} `json:"agentInfo"`
		} `json:"result"`
	}
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != 1 || resp.Result.ProtocolVersion != 1 || resp.Result.AgentInfo.Name == "" {
		t.Fatalf("unexpected initialize response: %+v", resp)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServeListenerWaitsWhenPoolFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- serveListener(ctx, ln, 1) }()

	first, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n"
	if _, err := first.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	var firstResp map[string]any
	if err := json.NewDecoder(first).Decode(&firstResp); err != nil {
		t.Fatal(err)
	}

	second, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := second.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	_ = second.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1)
	n, err := second.Read(buf)
	if ne, ok := err.(net.Error); !ok || !ne.Timeout() || n != 0 {
		t.Fatalf("expected second connection to wait, n=%d err=%v", n, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	_ = second.SetReadDeadline(time.Now().Add(2 * time.Second))
	var secondResp map[string]any
	if err := json.NewDecoder(second).Decode(&secondResp); err != nil {
		t.Fatal(err)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop")
	}
}
