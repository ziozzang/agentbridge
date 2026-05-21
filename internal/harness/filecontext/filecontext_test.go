package filecontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractMarkdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(path, []byte("# Notes\nhello"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Extract(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Name != "notes.md" || !strings.Contains(res.Text, "hello") || res.MimeType == "" {
		t.Fatalf("resource = %+v", res)
	}
}

func TestExtractRejectsBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blob.bin")
	if err := os.WriteFile(path, []byte{0, 1, 2, 3, 255}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(path); err == nil {
		t.Fatal("expected binary rejection")
	}
}

func TestExtractPDFPrintableFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doc.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7\nVisible text in fallback stream\x00\x01"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Extract(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "Visible text") {
		t.Fatalf("text = %q", res.Text)
	}
}
