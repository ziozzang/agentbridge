// Package googleoauth resolves Google/Vertex access tokens for provider use.
package googleoauth

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Resolver struct {
	CommandTimeout time.Duration
}

func New() *Resolver {
	return &Resolver{CommandTimeout: 10 * time.Second}
}

func (r *Resolver) Resolve(ctx context.Context) (string, error) {
	if v := envFirst("GOOGLE_OAUTH_ACCESS_TOKEN", "GOOGLE_VERTEX_ACCESS_TOKEN", "VERTEX_ACCESS_TOKEN", "GCLOUD_ACCESS_TOKEN"); v != "" {
		return v, nil
	}
	timeout := r.CommandTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for _, args := range [][]string{
		{"auth", "application-default", "print-access-token"},
		{"auth", "print-access-token"},
	} {
		out, err := exec.CommandContext(cmdCtx, "gcloud", args...).Output()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			return strings.TrimSpace(string(out)), nil
		}
	}
	return "", fmt.Errorf("google oauth: set GOOGLE_OAUTH_ACCESS_TOKEN/GOOGLE_VERTEX_ACCESS_TOKEN or install authenticated gcloud")
}

func envFirst(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}
