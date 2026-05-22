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

func completeSlashValue(value string, matches []string) string {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "/") || len(matches) == 0 {
		return ""
	}
	if len(matches) == 1 {
		return completionValue(matches[0])
	}
	prefix := commonPrefix(matches)
	if len(prefix) > len(value) {
		if hasMatch(prefix, matches) {
			return completionValue(prefix)
		}
		return prefix
	}
	return ""
}

func slashMatches(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "/") {
		return nil
	}
	var matches []string
	for _, item := range slashCommandSuggestions {
		if strings.HasPrefix(item, value) {
			matches = append(matches, item)
		}
	}
	return matches
}

func completionValue(match string) string {
	if strings.HasSuffix(match, " ") {
		return match
	}
	return match + " "
}

func hasMatch(match string, items []string) bool {
	for _, item := range items {
		if item == match {
			return true
		}
	}
	return false
}

func commonPrefix(items []string) string {
	if len(items) == 0 {
		return ""
	}
	prefix := items[0]
	for _, item := range items[1:] {
		for !strings.HasPrefix(item, prefix) && prefix != "" {
			prefix = prefix[:len(prefix)-1]
		}
	}
	return prefix
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
