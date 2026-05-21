package pii

import (
	"encoding/json"

	"github.com/ziozzang/agentbridge/internal/provider"
)

func MaskMessages(detector *Detector, messages []provider.Message, mask bool) ([]provider.Message, Mapping, bool) {
	if detector == nil || len(messages) == 0 {
		return messages, nil, false
	}
	out := append([]provider.Message(nil), messages...)
	builder := detector.NewBuilder()
	for i := range out {
		maskMessage(&out[i], builder, mask)
	}
	if !builder.Detected() {
		return messages, nil, false
	}
	if !mask {
		return messages, builder.Mapping(), true
	}
	return out, builder.Mapping(), true
}

func UnmaskChunk(ch provider.Chunk, mapping Mapping) provider.Chunk {
	if len(mapping) == 0 {
		return ch
	}
	ch.Text = Unmask(ch.Text, mapping)
	ch.Thinking = Unmask(ch.Thinking, mapping)
	if ch.ToolCall != nil {
		cp := *ch.ToolCall
		cp.Arguments = Unmask(cp.Arguments, mapping)
		ch.ToolCall = &cp
	}
	return ch
}

func maskMessage(m *provider.Message, b *MaskBuilder, mask bool) {
	m.Content = maskContent(m.Content, b, mask)
	for i := range m.ToolCalls {
		args := m.ToolCalls[i].Function.Arguments
		masked := b.Mask(args)
		if mask && masked != args {
			m.ToolCalls[i].Function.Arguments = masked
		}
	}
	if m.EncryptedContent != "" {
		masked := b.Mask(m.EncryptedContent)
		if mask && masked != m.EncryptedContent {
			m.EncryptedContent = masked
		}
	}
}

func maskContent(content any, b *MaskBuilder, mask bool) any {
	switch v := content.(type) {
	case string:
		masked := b.Mask(v)
		if mask {
			return masked
		}
		return v
	case []any:
		out := make([]any, len(v))
		copy(out, v)
		changed := false
		for i, item := range out {
			next, ok := maskContentPart(item, b, mask)
			if ok {
				out[i] = next
				changed = true
			}
		}
		if mask && changed {
			return out
		}
		return v
	case []map[string]any:
		out := make([]map[string]any, len(v))
		changed := false
		for i, item := range v {
			cp := cloneMap(item)
			if next, ok := maskContentPart(cp, b, mask); ok {
				if m, ok := next.(map[string]any); ok {
					cp = m
					changed = true
				}
			}
			out[i] = cp
		}
		if mask && changed {
			return out
		}
		return v
	default:
		if v == nil {
			return nil
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return v
		}
		masked := b.Mask(string(raw))
		if !mask || masked == string(raw) {
			return v
		}
		var out any
		if err := json.Unmarshal([]byte(masked), &out); err == nil {
			return out
		}
		return v
	}
}

func maskContentPart(item any, b *MaskBuilder, mask bool) (any, bool) {
	m, ok := item.(map[string]any)
	if !ok {
		return item, false
	}
	typ, _ := m["type"].(string)
	if typ != "" && typ != "text" && typ != "input_text" && typ != "output_text" {
		return item, false
	}
	changed := false
	cp := cloneMap(m)
	for _, key := range []string{"text", "input_text", "output_text", "content"} {
		if s, ok := cp[key].(string); ok {
			masked := b.Mask(s)
			if mask && masked != s {
				cp[key] = masked
				changed = true
			}
		}
	}
	return cp, changed
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
