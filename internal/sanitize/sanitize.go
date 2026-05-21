package sanitize

import (
	"regexp"
	"strings"
)

var DefaultTags = []string{"think", "thinking", "reasoning", "reflection"}

func Compile(tags []string) []*regexp.Regexp {
	if len(tags) == 0 {
		tags = DefaultTags
	}
	out := make([]*regexp.Regexp, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		q := regexp.QuoteMeta(tag)
		out = append(out, regexp.MustCompile(`(?is)<`+q+`\b[^>]*>.*?</\s*`+q+`\s*>`))
	}
	return out
}

func Strip(res []*regexp.Regexp, s string) string {
	if len(res) == 0 || s == "" {
		return s
	}
	out := s
	for _, re := range res {
		out = re.ReplaceAllString(out, "")
	}
	return strings.TrimLeft(out, " \t\r\n")
}

type StreamStripper struct {
	res    []*regexp.Regexp
	openRe *regexp.Regexp
	tags   []string
	buf    strings.Builder
}

func NewStreamStripper(res []*regexp.Regexp, tags []string) *StreamStripper {
	if len(res) == 0 {
		return &StreamStripper{}
	}
	if len(tags) == 0 {
		tags = DefaultTags
	}
	var alts []string
	cleaned := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(strings.ToLower(tag))
		if tag == "" {
			continue
		}
		cleaned = append(cleaned, tag)
		alts = append(alts, regexp.QuoteMeta(tag))
	}
	openRe := regexp.MustCompile(`(?is)<(` + strings.Join(alts, "|") + `)\b[^>]*>`)
	return &StreamStripper{res: res, openRe: openRe, tags: cleaned}
}

func (s *StreamStripper) Write(chunk string) string {
	if s == nil || len(s.res) == 0 {
		return chunk
	}
	s.buf.WriteString(chunk)
	cur := s.buf.String()
	for _, re := range s.res {
		cur = re.ReplaceAllString(cur, "")
	}
	if loc := s.openRe.FindStringIndex(cur); loc != nil {
		emit := cur[:loc[0]]
		s.buf.Reset()
		s.buf.WriteString(cur[loc[0]:])
		return emit
	}
	if i := strings.LastIndex(cur, "<"); i >= 0 {
		tail := cur[i:]
		if mightBecomeOpenTag(tail, s.tags) {
			s.buf.Reset()
			s.buf.WriteString(tail)
			return cur[:i]
		}
	}
	s.buf.Reset()
	return cur
}

func (s *StreamStripper) Flush() string {
	if s == nil || len(s.res) == 0 {
		return ""
	}
	out := s.buf.String()
	s.buf.Reset()
	return Strip(s.res, out)
}

func mightBecomeOpenTag(tail string, tags []string) bool {
	if len(tail) == 0 || tail[0] != '<' {
		return false
	}
	if len(tail) == 1 {
		return true
	}
	body := strings.ToLower(tail[1:])
	if strings.HasPrefix(body, "/") {
		return true
	}
	for _, tag := range tags {
		if strings.HasPrefix(tag, body) || strings.HasPrefix(body, tag) {
			return true
		}
	}
	return false
}
