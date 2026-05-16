package systemprompt

import (
	"os"
	"path/filepath"
)

func readFile(dir, name string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func getenv(name string) string { return os.Getenv(name) }
