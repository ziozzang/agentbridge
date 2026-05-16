// Package sessionstore persists chat sessions as one JSON file per session.
// It mirrors the TypeScript SessionStore: the directory is honoured via
// $ACP_GLM_SESSION_DIR / $XDG_STATE_HOME, file mode 0600 inside a 0700
// directory, schema version 1.
package sessionstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/ziozzang/glm-acp/internal/glm"
)

// SchemaVersion is the persistence format version.
const SchemaVersion = 1

// PersistedSession is the on-disk representation of a session.
type PersistedSession struct {
	SessionID     string        `json:"sessionId"`
	Cwd           string        `json:"cwd"`
	Messages      []glm.Message `json:"messages"`
	Title         *string       `json:"title"`
	UpdatedAt     string        `json:"updatedAt"`
	Model         string        `json:"model"`
	SchemaVersion int           `json:"schemaVersion"`
}

// Metadata is the lightweight session-list view.
type Metadata struct {
	SessionID string  `json:"sessionId"`
	Cwd       string  `json:"cwd"`
	Title     *string `json:"title"`
	UpdatedAt string  `json:"updatedAt"`
	Model     string  `json:"model"`
}

// Store is a file-backed session store.
type Store struct {
	Dir string
}

// DefaultDir resolves the directory we write session files to, honouring
// $ACP_GLM_SESSION_DIR / $XDG_STATE_HOME.
func DefaultDir() string {
	if v := os.Getenv("ACP_GLM_SESSION_DIR"); v != "" {
		return v
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "glm-acp-agent", "sessions")
}

// New returns a Store rooted at the default directory.
func New() *Store { return &Store{Dir: DefaultDir()} }

// NewIn returns a Store rooted at the given directory.
func NewIn(dir string) *Store { return &Store{Dir: dir} }

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func (s *Store) pathFor(sessionID string) (string, error) {
	if !idPattern.MatchString(sessionID) {
		return "", fmt.Errorf("invalid sessionId: %s", sessionID)
	}
	return filepath.Join(s.Dir, sessionID+".json"), nil
}

// Save persists a session, creating directories as needed.
func (s *Store) Save(sess PersistedSession) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	path, err := s.pathFor(sess.SessionID)
	if err != nil {
		return err
	}
	sess.SchemaVersion = SchemaVersion
	body, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return err
	}
	_ = os.Chmod(path, 0o600)
	return nil
}

// Load reads a session by id. Returns (nil, nil) if no such file exists.
func (s *Store) Load(sessionID string) (*PersistedSession, error) {
	path, err := s.pathFor(sessionID)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, nil // mirror TS swallow
	}
	var sess PersistedSession
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, nil
	}
	if sess.SchemaVersion != 0 && sess.SchemaVersion != SchemaVersion {
		return nil, nil
	}
	return &sess, nil
}

// ListMetadata enumerates all persisted sessions, newest-first by updatedAt.
func (s *Store) ListMetadata() []Metadata {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil
	}
	var out []Metadata
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		id := name[:len(name)-len(".json")]
		sess, err := s.Load(id)
		if err != nil || sess == nil {
			continue
		}
		out = append(out, Metadata{
			SessionID: sess.SessionID,
			Cwd:       sess.Cwd,
			Title:     sess.Title,
			UpdatedAt: sess.UpdatedAt,
			Model:     sess.Model,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}
