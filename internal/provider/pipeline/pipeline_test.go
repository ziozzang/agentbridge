package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/runtimeconfig"
)

type fakeProvider struct {
	seen []provider.Message
	opts provider.StreamOptions
}

func (f *fakeProvider) Name() string                          { return "fake" }
func (f *fakeProvider) Kind() string                          { return "fake" }
func (f *fakeProvider) AvailableModels() []provider.ModelInfo { return nil }
func (f *fakeProvider) DefaultModel() string                  { return "default" }
func (f *fakeProvider) ContextWindow(string) int              { return 1000 }
func (f *fakeProvider) StreamChat(_ context.Context, messages []provider.Message, opts provider.StreamOptions) (<-chan provider.Chunk, <-chan error) {
	f.seen = messages
	f.opts = opts
	placeholder := "[MASK_EMAIL_1_unknown]"
	if len(messages) > 0 {
		if s, ok := messages[0].Content.(string); ok {
			start := strings.Index(s, "[MASK_EMAIL_")
			if start >= 0 {
				if end := strings.Index(s[start:], "]"); end >= 0 {
					placeholder = s[start : start+end+1]
				}
			}
		}
	}
	chunks := make(chan provider.Chunk, 4)
	errs := make(chan error, 1)
	split := len(placeholder) / 2
	chunks <- provider.Chunk{Text: "masked " + placeholder[:split]}
	chunks <- provider.Chunk{Text: placeholder[split:] + " <think>hidden</think>OK"}
	chunks <- provider.Chunk{Done: true, StopReason: "stop"}
	close(chunks)
	errs <- nil
	close(errs)
	return chunks, errs
}

func TestWrapMasksUnmasksAndSanitizes(t *testing.T) {
	raw := &fakeProvider{}
	wrapped := Wrap(raw, runtimeconfig.Config{
		PII:      runtimeconfig.PII{Enabled: true},
		Sanitize: runtimeconfig.Sanitize{StripThinkTags: true},
	})
	chunks, errs := wrapped.StreamChat(context.Background(), []provider.Message{{Role: "user", Content: "email me at alice@example.com"}}, provider.StreamOptions{})
	var text string
	var done bool
	for ch := range chunks {
		text += ch.Text
		done = done || ch.Done
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if len(raw.seen) != 1 || strings.Contains(raw.seen[0].Content.(string), "alice@example.com") {
		t.Fatalf("upstream saw unmasked PII: %#v", raw.seen)
	}
	if !strings.Contains(text, "alice@example.com") {
		t.Fatalf("response was not unmasked: %q", text)
	}
	if strings.Contains(text, "<think>") || strings.Contains(text, "hidden") {
		t.Fatalf("thinking tags were not stripped: %q", text)
	}
	if !done {
		t.Fatal("done chunk was not preserved")
	}
}

func TestWrapRejectsPII(t *testing.T) {
	wrapped := Wrap(&fakeProvider{}, runtimeconfig.Config{
		PII: runtimeconfig.PII{Enabled: true, Routing: runtimeconfig.PIIRouting{Reject: true, RejectMessage: "blocked"}},
	})
	chunks, errs := wrapped.StreamChat(context.Background(), []provider.Message{{Role: "user", Content: "alice@example.com"}}, provider.StreamOptions{})
	for range chunks {
		t.Fatal("unexpected chunk")
	}
	if err := <-errs; err == nil || err.Error() != "blocked" {
		t.Fatalf("err=%v", err)
	}
}

func TestWrapMasksEnvFileSecrets(t *testing.T) {
	tmp := t.TempDir()
	envPath := filepath.Join(tmp, "env")
	if err := os.WriteFile(envPath, []byte("export OPENAI_API_KEY=ollama-cloud-secret-value-123456\nSHORT=small\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := &fakeProvider{}
	wrapped := Wrap(raw, runtimeconfig.Config{
		PII: runtimeconfig.PII{
			Enabled: true,
			Env: runtimeconfig.PIIEnv{
				Enabled:   true,
				File:      envPath,
				Names:     []string{"OPENAI_API_KEY"},
				MinLength: 12,
			},
		},
	})
	chunks, errs := wrapped.StreamChat(context.Background(), []provider.Message{{Role: "user", Content: "key ollama-cloud-secret-value-123456"}}, provider.StreamOptions{})
	for range chunks {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if len(raw.seen) != 1 || strings.Contains(raw.seen[0].Content.(string), "ollama-cloud-secret-value-123456") {
		t.Fatalf("upstream saw env secret: %#v", raw.seen)
	}
	if !strings.Contains(raw.seen[0].Content.(string), "[MASK_ENV_SECRET_") {
		t.Fatalf("env secret was not masked: %#v", raw.seen)
	}
}

func TestWrapMasksAllLongEnvFileValuesByDefault(t *testing.T) {
	tmp := t.TempDir()
	envPath := filepath.Join(tmp, "env")
	if err := os.WriteFile(envPath, []byte("CUSTOM_SECRET=opaque-secret-value-abcdef\nSHORT=small\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := &fakeProvider{}
	wrapped := Wrap(raw, runtimeconfig.Config{
		PII: runtimeconfig.PII{
			Enabled: true,
			Env: runtimeconfig.PIIEnv{
				File:      envPath,
				MinLength: 12,
			},
		},
	})
	chunks, errs := wrapped.StreamChat(context.Background(), []provider.Message{{Role: "user", Content: "secret opaque-secret-value-abcdef small"}}, provider.StreamOptions{})
	for range chunks {
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	got := raw.seen[0].Content.(string)
	if strings.Contains(got, "opaque-secret-value-abcdef") {
		t.Fatalf("upstream saw env secret: %q", got)
	}
	if strings.Contains(got, "[MASK_ENV_SECRET_") == false {
		t.Fatalf("env secret was not masked: %q", got)
	}
	if !strings.Contains(got, "small") {
		t.Fatalf("short env value should not be masked: %q", got)
	}
}
