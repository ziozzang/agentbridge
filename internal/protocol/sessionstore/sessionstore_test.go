package sessionstore

import (
	"strings"
	"testing"

	"github.com/ziozzang/glm-acp/internal/glm"
)

func ptr(s string) *string { return &s }

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewIn(dir)
	want := PersistedSession{
		SessionID: "abc-123",
		Cwd:       "/tmp",
		Messages:  []glm.Message{{Role: "user", Content: "hi"}},
		Title:     ptr("greeting"),
		UpdatedAt: "2024-01-01T00:00:00Z",
		Model:     "glm-5.1",
	}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("abc-123")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.SessionID != want.SessionID || got.Cwd != want.Cwd ||
		got.UpdatedAt != want.UpdatedAt || got.Model != want.Model ||
		got.Title == nil || *got.Title != "greeting" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Errorf("schema version not stamped: %d", got.SchemaVersion)
	}
}

func TestLoadMissingReturnsNil(t *testing.T) {
	s := NewIn(t.TempDir())
	got, err := s.Load("nope")
	if err != nil || got != nil {
		t.Errorf("got (%v,%v)", got, err)
	}
}

func TestPathRejectsTraversal(t *testing.T) {
	s := NewIn(t.TempDir())
	bad := []string{"../escape", "a/b", ".", "name.with.dots"}
	for _, b := range bad {
		if _, err := s.pathFor(b); err == nil {
			t.Errorf("expected reject for %q", b)
		}
	}
}

func TestListMetadataSortedByUpdatedAtDesc(t *testing.T) {
	dir := t.TempDir()
	s := NewIn(dir)
	_ = s.Save(PersistedSession{SessionID: "a", Cwd: "/x", UpdatedAt: "2024-01-01", Model: "m"})
	_ = s.Save(PersistedSession{SessionID: "b", Cwd: "/x", UpdatedAt: "2024-03-01", Model: "m"})
	_ = s.Save(PersistedSession{SessionID: "c", Cwd: "/x", UpdatedAt: "2024-02-01", Model: "m"})
	meta := s.ListMetadata()
	if len(meta) != 3 {
		t.Fatalf("len = %d", len(meta))
	}
	if meta[0].SessionID != "b" || meta[1].SessionID != "c" || meta[2].SessionID != "a" {
		t.Errorf("order wrong: %+v", meta)
	}
}

func TestDefaultDirHonoursOverride(t *testing.T) {
	t.Setenv("ACP_GLM_SESSION_DIR", "/some/dir")
	if !strings.Contains(DefaultDir(), "some/dir") {
		t.Errorf("override ignored: %s", DefaultDir())
	}
}
