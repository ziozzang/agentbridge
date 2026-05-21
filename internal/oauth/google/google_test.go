package googleoauth

import (
	"context"
	"testing"
)

func TestResolvePrefersEnv(t *testing.T) {
	t.Setenv("GOOGLE_OAUTH_ACCESS_TOKEN", "token")
	got, err := New().Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "token" {
		t.Fatalf("got %q", got)
	}
}
