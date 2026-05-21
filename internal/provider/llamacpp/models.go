package llamacpp

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ziozzang/agentbridge/internal/provider"
)

func (c *Client) models(fetch bool) []provider.ModelInfo {
	if len(c.cfg.Models) > 0 {
		out := make([]provider.ModelInfo, len(c.cfg.Models))
		copy(out, c.cfg.Models)
		return out
	}
	if !fetch {
		if m := strings.TrimSpace(c.cfg.DefaultModel); m != "" {
			return []provider.ModelInfo{{ModelID: m, Name: m, Provider: c.Name()}}
		}
	}
	if fetched, err := c.fetchModels(); err == nil && len(fetched) > 0 {
		return fetched
	}
	if m := strings.TrimSpace(c.cfg.DefaultModel); m != "" {
		return []provider.ModelInfo{{ModelID: m, Name: m, Provider: c.Name()}}
	}
	return nil
}

func (c *Client) fetchModels() ([]provider.ModelInfo, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(c.cfg.BaseURL, "/")+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	c.setAuth(req)
	client := c.httpClient()
	if c.HTTPClient == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return nil, nil
	}
	var payload struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
			Meta    struct {
				Context int `json:"n_ctx"`
			} `json:"meta"`
		} `json:"data"`
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := make([]provider.ModelInfo, 0, len(payload.Data)+len(payload.Models))
	for _, m := range payload.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		out = append(out, provider.ModelInfo{ModelID: id, Name: id, Provider: firstNonEmpty(m.OwnedBy, c.Name()), ContextWindow: m.Meta.Context})
	}
	if len(out) > 0 {
		return out, nil
	}
	for _, m := range payload.Models {
		id := firstNonEmpty(m.Model, m.Name)
		if id == "" {
			continue
		}
		out = append(out, provider.ModelInfo{ModelID: id, Name: id, Provider: c.Name()})
	}
	return out, nil
}
