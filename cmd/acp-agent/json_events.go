package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

type jsonEventSink struct {
	done chan struct{}
	once sync.Once
	wg   sync.WaitGroup
}

func startJSONEventSink(c *client, w io.Writer) *jsonEventSink {
	sink := &jsonEventSink{done: make(chan struct{})}
	c.events = make(chan uiEvent, 4096)
	c.stream = nil
	sink.wg.Add(1)
	go func() {
		defer sink.wg.Done()
		enc := json.NewEncoder(w)
		for {
			select {
			case ev := <-c.events:
				_ = enc.Encode(uiEventRecord(ev))
			case <-sink.done:
				for {
					select {
					case ev := <-c.events:
						_ = enc.Encode(uiEventRecord(ev))
					default:
						return
					}
				}
			}
		}
	}()
	return sink
}

func (s *jsonEventSink) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() { close(s.done) })
	s.wg.Wait()
}

func uiEventRecord(ev uiEvent) map[string]any {
	out := map[string]any{
		"ts": time.Now().UTC().Format(time.RFC3339Nano),
	}
	switch ev := ev.(type) {
	case uiStateEvent:
		out["type"] = "state"
		out["state"] = ev.State
		out["options"] = ev.Opts
	case uiUserEvent:
		out["type"] = "user"
		out["text"] = ev.Text
	case uiAssistantDeltaEvent:
		out["type"] = "assistant_delta"
		out["text"] = ev.Text
	case uiThinkingDeltaEvent:
		out["type"] = "thinking_delta"
		out["text"] = ev.Text
	case uiToolEvent:
		out["type"] = "tool"
		out["status"] = ev.Status
		out["title"] = ev.Title
		out["detail"] = ev.Detail
	case uiInfoEvent:
		out["type"] = "info"
		out["title"] = ev.Title
		out["body"] = ev.Body
	case uiErrorEvent:
		out["type"] = "error"
		out["message"] = ev.Message
	case uiPermissionRequest:
		out["type"] = "permission_request"
		out["title"] = ev.Title
		out["detail"] = ev.Detail
		out["options"] = ev.Options
	default:
		out["type"] = "unknown"
		out["value"] = fmt.Sprint(ev)
	}
	return out
}

func jsonEventRepl(ctx context.Context, c *client) error {
	scanner := bufio.NewScanner(c.stdin)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "/exit" || line == "/quit" {
			return nil
		}
		if err := c.runCommand(ctx, line); err != nil {
			return err
		}
	}
	return scanner.Err()
}
