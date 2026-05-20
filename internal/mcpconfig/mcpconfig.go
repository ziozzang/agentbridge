package mcpconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ziozzang/agentbridge/internal/acp"
)

type fileConfig struct {
	MCPServers      flexibleServers `json:"mcp_servers" yaml:"mcp_servers"`
	MCPServersCamel flexibleServers `json:"mcpServers" yaml:"mcpServers"`
}

type server struct {
	Type     string            `json:"type" yaml:"type"`
	Name     string            `json:"name" yaml:"name"`
	URL      string            `json:"url" yaml:"url"`
	Headers  map[string]string `json:"headers" yaml:"headers"`
	Disabled bool              `json:"disabled" yaml:"disabled"`
	Enabled  *bool             `json:"enabled" yaml:"enabled"`
}

type flexibleServers []server

func (s *flexibleServers) UnmarshalJSON(data []byte) error {
	var list []server
	if err := json.Unmarshal(data, &list); err == nil {
		*s = list
		return nil
	}
	var byName map[string]server
	if err := json.Unmarshal(data, &byName); err != nil {
		return err
	}
	out := make([]server, 0, len(byName))
	for name, srv := range byName {
		if srv.Name == "" {
			srv.Name = name
		}
		out = append(out, srv)
	}
	*s = out
	return nil
}

func (s *flexibleServers) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.SequenceNode:
		var list []server
		if err := value.Decode(&list); err != nil {
			return err
		}
		*s = list
		return nil
	case yaml.MappingNode:
		var byName map[string]server
		if err := value.Decode(&byName); err != nil {
			return err
		}
		out := make([]server, 0, len(byName))
		for name, srv := range byName {
			if srv.Name == "" {
				srv.Name = name
			}
			out = append(out, srv)
		}
		*s = out
		return nil
	default:
		return fmt.Errorf("expected MCP servers list or map")
	}
}

func Load() ([]acp.McpServer, error) {
	path := firstNonEmpty(os.Getenv("AGENTBRIDGE_MCP_FILE"), os.Getenv("AGENTBRIDGE_MCP_CONFIG"))
	if path == "" {
		path = defaultPath()
	}
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("mcpconfig: read %s: %w", path, err)
	}
	var cfg fileConfig
	switch {
	case strings.HasSuffix(path, ".yaml"), strings.HasSuffix(path, ".yml"):
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("mcpconfig: parse %s: %w", path, err)
		}
	default:
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("mcpconfig: parse %s: %w", path, err)
		}
	}
	servers := append([]server{}, cfg.MCPServers...)
	servers = append(servers, cfg.MCPServersCamel...)
	disabled := disabledSet(os.Getenv("AGENTBRIDGE_DISABLED_MCPS"))
	out := make([]acp.McpServer, 0, len(servers))
	for _, srv := range servers {
		if srv.Disabled || (srv.Enabled != nil && !*srv.Enabled) || disabled[strings.ToLower(srv.Name)] {
			continue
		}
		if srv.Type == "" {
			srv.Type = "http"
		}
		if srv.Name == "" || srv.URL == "" {
			continue
		}
		headers := map[string]string{}
		for k, v := range srv.Headers {
			headers[k] = os.ExpandEnv(v)
		}
		out = append(out, acp.McpServer{
			Type: strings.ToLower(srv.Type), Name: srv.Name, URL: os.ExpandEnv(srv.URL), Headers: headers,
		})
	}
	return out, nil
}

func defaultPath() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		for _, name := range []string{"mcp.yaml", "mcp.json"} {
			p := filepath.Join(dir, "agentbridge", name)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

func disabledSet(spec string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.FieldsFunc(spec, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		if s := strings.ToLower(strings.TrimSpace(part)); s != "" {
			out[s] = true
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
