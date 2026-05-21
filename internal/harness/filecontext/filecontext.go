// Package filecontext extracts local files into text resources that can be
// attached to ACP prompts.
package filecontext

import (
	"bytes"
	"fmt"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxExtractedChars = 256 * 1024

// Resource is an extracted file ready to send as prompt context.
type Resource struct {
	Path      string
	Name      string
	MimeType  string
	Text      string
	Size      int64
	Truncated bool
}

// Extract reads path and converts supported documents to text.
func Extract(path string) (Resource, error) {
	clean, err := filepath.Abs(path)
	if err != nil {
		return Resource{}, err
	}
	st, err := os.Stat(clean)
	if err != nil {
		return Resource{}, err
	}
	if st.IsDir() {
		return Resource{}, fmt.Errorf("cannot attach directory: %s", clean)
	}
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(clean)))
	if mimeType == "" {
		mimeType = "text/plain"
	}
	var text string
	if strings.EqualFold(filepath.Ext(clean), ".pdf") {
		text, err = extractPDF(clean)
	} else {
		text, err = extractTextFile(clean)
	}
	if err != nil {
		return Resource{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return Resource{}, fmt.Errorf("no extractable text: %s", clean)
	}
	truncated := false
	if len(text) > MaxExtractedChars {
		text = text[:MaxExtractedChars]
		truncated = true
	}
	return Resource{
		Path:      clean,
		Name:      filepath.Base(clean),
		MimeType:  mimeType,
		Text:      text,
		Size:      st.Size(),
		Truncated: truncated,
	}, nil
}

func extractTextFile(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if utf8.Valid(body) && mostlyText(string(body)) {
		return string(body), nil
	}
	return "", fmt.Errorf("unsupported binary file: %s", path)
}

func extractPDF(path string) (string, error) {
	if bin, err := exec.LookPath("pdftotext"); err == nil {
		cmd := exec.Command(bin, "-layout", path, "-")
		var out, stderr bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		if err := cmd.Run(); err == nil && strings.TrimSpace(out.String()) != "" {
			return out.String(), nil
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := printableRuns(body)
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("PDF text extraction requires pdftotext for this file: %s", path)
	}
	return text, nil
}

func mostlyText(s string) bool {
	if s == "" {
		return false
	}
	total, bad := 0, 0
	for _, r := range s {
		total++
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r == utf8.RuneError || (!unicode.IsPrint(r) && !unicode.IsSpace(r)) {
			bad++
		}
	}
	return total > 0 && bad*20 < total
}

func printableRuns(body []byte) string {
	var b strings.Builder
	run := make([]byte, 0, 128)
	flush := func() {
		if len(run) >= 5 {
			b.Write(run)
			b.WriteByte('\n')
		}
		run = run[:0]
	}
	for _, c := range body {
		if c == '\n' || c == '\r' || c == '\t' || (c >= 32 && c <= 126) {
			run = append(run, c)
			continue
		}
		flush()
	}
	flush()
	return b.String()
}
