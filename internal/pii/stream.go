package pii

import "strings"

type StreamUnmasker struct {
	mapping Mapping
	maxTail int
	buf     strings.Builder
}

func NewStreamUnmasker(mapping Mapping, maxTokenLen int) *StreamUnmasker {
	if maxTokenLen <= 0 {
		maxTokenLen = 64
	}
	return &StreamUnmasker{mapping: mapping, maxTail: maxTokenLen - 1}
}

func (u *StreamUnmasker) Write(chunk string) string {
	if u == nil || len(u.mapping) == 0 || chunk == "" {
		return chunk
	}
	u.buf.WriteString(chunk)
	cur := Unmask(u.buf.String(), u.mapping)
	if len(cur) <= u.maxTail {
		u.buf.Reset()
		u.buf.WriteString(cur)
		return ""
	}
	emitLen := len(cur) - u.maxTail
	emit := cur[:emitLen]
	u.buf.Reset()
	u.buf.WriteString(cur[emitLen:])
	return emit
}

func (u *StreamUnmasker) Flush() string {
	if u == nil {
		return ""
	}
	out := Unmask(u.buf.String(), u.mapping)
	u.buf.Reset()
	return out
}
