// Package skills discovers and loads markdown snippets that can be injected
// into agent sessions at runtime.
package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Skill is a discovered markdown skill.
type Skill struct {
	Name string
	Path string
	Body string
	Hash string
}

var skillNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// List returns skills from the project and user config skill directories.
// Project-local skills win when duplicate names exist.
func List(cwd string) ([]Skill, error) {
	var out []Skill
	seen := map[string]bool{}
	for _, dir := range searchDirs(cwd) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || strings.ToLower(filepath.Ext(e.Name())) != ".md" {
				continue
			}
			name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
			if !skillNamePattern.MatchString(name) || seen[name] {
				continue
			}
			s, err := loadPath(filepath.Join(dir, e.Name()), name)
			if err != nil {
				return nil, err
			}
			out = append(out, s)
			seen[name] = true
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Load loads one named skill from the project or user config directory.
func Load(cwd, name string) (Skill, error) {
	name = strings.TrimSpace(name)
	if !skillNamePattern.MatchString(name) {
		return Skill{}, fmt.Errorf("invalid skill name: %s", name)
	}
	for _, dir := range searchDirs(cwd) {
		path := filepath.Join(dir, name+".md")
		if _, err := os.Stat(path); err == nil {
			return loadPath(path, name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return Skill{}, err
		}
	}
	return Skill{}, fmt.Errorf("skill not found: %s", name)
}

// LoadPath loads an already-resolved skill path. It is used for active skills
// persisted with their original path.
func LoadPath(path, fallbackName string) (Skill, error) {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if name == "" {
		name = fallbackName
	}
	if !skillNamePattern.MatchString(name) {
		name = fallbackName
	}
	return loadPath(path, name)
}

func loadPath(path, name string) (Skill, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	sum := sha256.Sum256(body)
	return Skill{
		Name: name,
		Path: path,
		Body: string(body),
		Hash: hex.EncodeToString(sum[:]),
	}, nil
}

func searchDirs(cwd string) []string {
	var dirs []string
	if cwd != "" {
		dirs = append(dirs, filepath.Join(cwd, ".agentbridge", "skills"))
	}
	if cfg, err := os.UserConfigDir(); err == nil && cfg != "" {
		dirs = append(dirs, filepath.Join(cfg, "agentbridge", "skills"))
	}
	return dirs
}
