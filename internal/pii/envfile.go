package pii

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

type EnvSecret struct {
	Name  string
	Value string
}

func LoadEnvSecrets(path string, names []string, minLength int) ([]EnvSecret, error) {
	if minLength <= 0 {
		minLength = 12
	}
	path = expandPath(path)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	allow := map[string]struct{}{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			allow[name] = struct{}{}
		}
	}
	var out []EnvSecret
	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		name, value, ok := parseEnvLine(scanner.Text())
		if !ok {
			continue
		}
		if len(allow) > 0 {
			if _, ok := allow[name]; !ok {
				continue
			}
		}
		if len(value) < minLength || strings.TrimSpace(value) == "" {
			continue
		}
		key := name + "\x00" + value
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, EnvSecret{Name: name, Value: value})
	}
	return out, scanner.Err()
}

func parseEnvLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	idx := strings.Index(line, "=")
	if idx <= 0 {
		return "", "", false
	}
	name := strings.TrimSpace(line[:idx])
	value := strings.TrimSpace(line[idx+1:])
	if name == "" || strings.ContainsAny(name, " \t") {
		return "", "", false
	}
	value = stripInlineComment(value)
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		q := value[0]
		if (q == '\'' || q == '"') && value[len(value)-1] == q {
			value = value[1 : len(value)-1]
		}
	}
	return name, value, true
}

func stripInlineComment(value string) string {
	inSingle := false
	inDouble := false
	escaped := false
	for i, r := range value {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && inDouble {
			escaped = true
			continue
		}
		if r == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if r == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if r == '#' && !inSingle && !inDouble && (i == 0 || value[i-1] == ' ' || value[i-1] == '\t') {
			return value[:i]
		}
	}
	return value
}

func expandPath(path string) string {
	path = os.ExpandEnv(strings.TrimSpace(path))
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}
