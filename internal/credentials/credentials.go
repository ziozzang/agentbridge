// Package credentials resolves the Z.AI API key from the environment or the
// XDG credentials file written by the `--setup` flow. It mirrors the
// JavaScript implementation: env var wins over the credentials file, and the
// file is created with mode 0600 in a 0700 parent directory.
package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ziozzang/glm-acp/internal/logger"
)

// fileBody is the JSON shape persisted on disk.
type fileBody struct {
	APIKey string `json:"z_ai_api_key"`
}

// Path returns the absolute path of the credentials JSON file. It honours
// $XDG_CONFIG_HOME and falls back to "~/.config".
func Path() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "glm-acp-agent", "credentials.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".config", "glm-acp-agent", "credentials.json")
}

// ReadFromFile reads the API key from the given path (or the default path
// when path is empty). Missing or malformed files yield ("", nil).
func ReadFromFile(path string) (string, error) {
	if path == "" {
		path = Path()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", nil // mirror TS: any read failure is silent
	}
	var body fileBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", nil
	}
	return body.APIKey, nil
}

// Resolve returns the API key from the environment when set, falling back to
// the credentials file. Returns "" if neither source has a key.
func Resolve() string {
	if env := os.Getenv("Z_AI_API_KEY"); env != "" {
		logger.Debugf("resolveApiKey: source=env key=%s", logger.MaskSecret(env))
		return env
	}
	key, _ := ReadFromFile("")
	if key != "" {
		logger.Debugf("resolveApiKey: source=file key=%s", logger.MaskSecret(key))
	} else {
		logger.Debug("resolveApiKey: no key found")
	}
	return key
}

// Write persists the API key to the credentials file at path (or the default
// path when path is empty). Empty keys are rejected. Parent directory is
// created with mode 0700; the file itself is written with mode 0600.
func Write(apiKey, path string) error {
	if apiKey == "" {
		return errors.New("refusing to write empty API key")
	}
	if path == "" {
		path = Path()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create credentials dir: %w", err)
	}
	body, err := json.MarshalIndent(fileBody{APIKey: apiKey}, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	// Re-chmod in case umask altered the requested mode (some platforms).
	_ = os.Chmod(path, 0o600)
	return nil
}
