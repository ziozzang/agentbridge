package main

import "strings"

type tuiCompletionSurface struct {
	value   string
	matches []string
	active  bool
}

func (s tuiCompletionSurface) Hint() string {
	if !s.active {
		return ""
	}
	value := s.value
	for _, hint := range commandArgumentHints {
		if strings.HasPrefix(value, hint.prefix) {
			return hint.text
		}
	}
	if len(s.matches) == 0 {
		return ""
	}
	if group := compactSlashHint(value, s.matches); group != "" {
		return group
	}
	visible := s.matches
	if len(visible) > 5 {
		visible = visible[:5]
	}
	return "tab complete: " + strings.Join(visible, "  ")
}

func compactSlashHint(value string, matches []string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "/") {
		return ""
	}
	root := value
	if i := strings.IndexByte(strings.TrimPrefix(root, "/"), ' '); i >= 0 {
		root = root[:i+1]
	}
	seen := map[string]bool{}
	var args []string
	for _, item := range matches {
		if item == root {
			continue
		}
		if strings.HasPrefix(item, root+" ") {
			arg := strings.TrimSpace(strings.TrimPrefix(item, root))
			if arg == "" || strings.Contains(arg, " ") || seen[arg] {
				continue
			}
			seen[arg] = true
			args = append(args, arg)
		}
	}
	if len(args) == 0 {
		return ""
	}
	return root + " " + strings.Join(args, "|")
}

func (m tuiModel) completionSurface() tuiCompletionSurface {
	if m.overlay != nil {
		return tuiCompletionSurface{}
	}
	return tuiCompletionSurface{
		value:   m.input.Value(),
		matches: m.input.MatchedSuggestions(),
		active:  true,
	}
}

var commandArgumentHints = []struct {
	prefix string
	text   string
}{
	{prefix: "/permission ", text: "/permission allow|deny|reject|prompt|cancel"},
	{prefix: "/goal ", text: "/goal status|set|run|clear"},
	{prefix: "/thinking ", text: "/thinking on|off|toggle"},
	{prefix: "/tools ", text: "/tools on|off|toggle"},
	{prefix: "/raw ", text: "/raw on|off|toggle"},
	{prefix: "/mode ", text: "/mode default|accept_edits|bypass_permissions"},
}
