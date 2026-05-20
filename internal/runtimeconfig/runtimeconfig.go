package runtimeconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server Server `yaml:"server"`
}

type Server struct {
	Enabled    bool   `yaml:"enabled"`
	Listen     string `yaml:"listen"`
	PoolSize   int    `yaml:"pool_size"`
	WaitSize   *int   `yaml:"wait_size"`
	HTTPListen string `yaml:"http_listen"`
	GRPCListen string `yaml:"grpc_listen"`
}

func Defaults() Config {
	return Config{Server: Server{Listen: "127.0.0.1:8765", PoolSize: 4}}
}

func Load() (Config, error) {
	cfg := Defaults()
	path := firstNonEmpty(os.Getenv("AGENTBRIDGE_CONFIG_FILE"), os.Getenv("ACP_HARNESS_CONFIG_FILE"))
	if path == "" {
		path = defaultPath()
	}
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("runtimeconfig: read %s: %w", path, err)
	}
	expanded := os.ExpandEnv(string(data))
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return cfg, fmt.Errorf("runtimeconfig: parse %s: %w", path, err)
	}
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = "127.0.0.1:8765"
	}
	if cfg.Server.PoolSize == 0 {
		cfg.Server.PoolSize = 4
	}
	return cfg, nil
}

func defaultPath() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		p := filepath.Join(dir, "agentbridge", "config.yaml")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
