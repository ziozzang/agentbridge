// Package bedrock implements Amazon Bedrock Converse over AWS SigV4.
package bedrock

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ziozzang/agentbridge/internal/provider"
	"github.com/ziozzang/agentbridge/internal/tools/definitions"
)

const Kind = "bedrock-converse"

func init() {
	provider.Register(Kind, func(cfg provider.Config) (provider.Provider, error) {
		return New(cfg), nil
	})
}

type Client struct {
	cfg        provider.Config
	HTTPClient *http.Client
}

func New(cfg provider.Config) *Client {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 8192
	}
	return &Client{cfg: cfg, HTTPClient: http.DefaultClient}
}

func (c *Client) Name() string { return firstNonEmpty(c.cfg.Name, "amazon-bedrock") }
func (c *Client) Kind() string { return Kind }
func (c *Client) AvailableModels() []provider.ModelInfo {
	out := make([]provider.ModelInfo, len(c.cfg.Models))
	copy(out, c.cfg.Models)
	return out
}
func (c *Client) DefaultModel() string {
	if c.cfg.DefaultModel != "" {
		return c.cfg.DefaultModel
	}
	if len(c.cfg.Models) > 0 {
		return c.cfg.Models[0].ModelID
	}
	return ""
}
func (c *Client) ContextWindow(model string) int {
	_ = model
	if c.cfg.ContextWindow > 0 {
		return c.cfg.ContextWindow
	}
	return 200000
}

type converseRequest struct {
	Messages        []bedrockMessage `json:"messages"`
	System          []bedrockBlock   `json:"system,omitempty"`
	InferenceConfig *inferenceConfig `json:"inferenceConfig,omitempty"`
	ToolConfig      *toolConfig      `json:"toolConfig,omitempty"`
}

type inferenceConfig struct {
	MaxTokens int `json:"maxTokens,omitempty"`
}

type bedrockMessage struct {
	Role    string         `json:"role"`
	Content []bedrockBlock `json:"content"`
}

type bedrockBlock struct {
	Text       string         `json:"text,omitempty"`
	ToolUse    *toolUse       `json:"toolUse,omitempty"`
	ToolResult *toolResult    `json:"toolResult,omitempty"`
	JSON       map[string]any `json:"json,omitempty"`
}

type toolUse struct {
	ToolUseID string         `json:"toolUseId"`
	Name      string         `json:"name"`
	Input     map[string]any `json:"input,omitempty"`
}

type toolResult struct {
	ToolUseID string         `json:"toolUseId"`
	Content   []bedrockBlock `json:"content"`
}

type toolConfig struct {
	Tools []bedrockTool `json:"tools,omitempty"`
}

type bedrockTool struct {
	ToolSpec toolSpec `json:"toolSpec"`
}

type toolSpec struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	InputSchema inputSchema `json:"inputSchema"`
}

type inputSchema struct {
	JSON json.RawMessage `json:"json"`
}

type converseResponse struct {
	Output struct {
		Message bedrockMessage `json:"message"`
	} `json:"output"`
	StopReason string `json:"stopReason"`
	Usage      struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		TotalTokens  int `json:"totalTokens"`
	} `json:"usage"`
}

func (c *Client) StreamChat(ctx context.Context, messages []provider.Message, opts provider.StreamOptions) (<-chan provider.Chunk, <-chan error) {
	chunks := make(chan provider.Chunk, 16)
	errs := make(chan error, 1)
	go func() {
		defer close(chunks)
		defer close(errs)
		model := firstNonEmpty(opts.Model, c.DefaultModel())
		system, outMessages := translateMessages(messages)
		reqBody := converseRequest{
			Messages:        outMessages,
			System:          system,
			InferenceConfig: &inferenceConfig{MaxTokens: c.cfg.MaxTokens},
			ToolConfig:      translateTools(opts.Tools),
		}
		body, err := json.Marshal(reqBody)
		if err != nil {
			errs <- err
			return
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.converseURL(model), bytes.NewReader(body))
		if err != nil {
			errs <- err
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		for k, v := range c.cfg.Headers {
			req.Header.Set(k, v)
		}
		if err := c.sign(req, body); err != nil {
			errs <- err
			return
		}
		resp, err := c.httpClient().Do(req)
		if err != nil {
			errs <- err
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			errs <- fmt.Errorf("%s HTTP %d: %s", c.Name(), resp.StatusCode, strings.TrimSpace(string(b)))
			return
		}
		var out converseResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			errs <- err
			return
		}
		var calls []provider.ToolCall
		for _, block := range out.Output.Message.Content {
			if block.Text != "" {
				chunks <- provider.Chunk{Text: block.Text}
			}
			if block.ToolUse != nil {
				args, _ := json.Marshal(block.ToolUse.Input)
				calls = append(calls, provider.ToolCall{ID: block.ToolUse.ToolUseID, Name: block.ToolUse.Name, Arguments: string(args)})
			}
		}
		for _, call := range calls {
			cp := call
			chunks <- provider.Chunk{ToolCall: &cp}
		}
		if out.Usage.TotalTokens != 0 || out.Usage.InputTokens != 0 || out.Usage.OutputTokens != 0 {
			chunks <- provider.Chunk{Usage: &provider.Usage{
				InputTokens:  out.Usage.InputTokens,
				OutputTokens: out.Usage.OutputTokens,
				TotalTokens:  out.Usage.TotalTokens,
			}}
		}
		stop := out.StopReason
		if stop == "tool_use" || stop == "toolUse" {
			stop = "tool_calls"
		}
		chunks <- provider.Chunk{Done: true, StopReason: firstNonEmpty(stop, "end_turn")}
	}()
	return chunks, errs
}

func (c *Client) converseURL(model string) string {
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	if base == "" {
		base = "https://bedrock-runtime." + c.region() + ".amazonaws.com"
	}
	return base + "/model/" + url.PathEscape(model) + "/converse"
}

func (c *Client) region() string {
	return firstNonEmpty(c.extraString("region"), os.Getenv("AWS_REGION"), os.Getenv("AWS_DEFAULT_REGION"), "us-east-1")
}

func translateMessages(in []provider.Message) ([]bedrockBlock, []bedrockMessage) {
	var system []bedrockBlock
	var out []bedrockMessage
	toolNames := map[string]string{}
	for _, m := range in {
		switch m.Role {
		case "system":
			if s := contentToString(m.Content); s != "" {
				system = append(system, bedrockBlock{Text: s})
			}
		case "assistant":
			parts := []bedrockBlock{}
			if s := contentToString(m.Content); s != "" {
				parts = append(parts, bedrockBlock{Text: s})
			}
			for _, tc := range m.ToolCalls {
				args := map[string]any{}
				_ = json.Unmarshal([]byte(firstNonEmpty(tc.Function.Arguments, "{}")), &args)
				parts = append(parts, bedrockBlock{ToolUse: &toolUse{ToolUseID: tc.ID, Name: tc.Function.Name, Input: args}})
				toolNames[tc.ID] = tc.Function.Name
			}
			if len(parts) > 0 {
				out = append(out, bedrockMessage{Role: "assistant", Content: parts})
			}
		case "tool":
			name := firstNonEmpty(toolNames[m.ToolCallID], m.Name, m.ToolCallID)
			_ = name
			out = append(out, bedrockMessage{Role: "user", Content: []bedrockBlock{{ToolResult: &toolResult{
				ToolUseID: m.ToolCallID,
				Content:   []bedrockBlock{{Text: contentToString(m.Content)}},
			}}}})
		default:
			out = append(out, bedrockMessage{Role: "user", Content: []bedrockBlock{{Text: contentToString(m.Content)}}})
		}
	}
	return system, out
}

func translateTools(in []definitions.Tool) *toolConfig {
	if len(in) == 0 {
		return nil
	}
	tools := make([]bedrockTool, 0, len(in))
	for _, t := range in {
		params := t.Function.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, bedrockTool{ToolSpec: toolSpec{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: inputSchema{JSON: params},
		}})
	}
	return &toolConfig{Tools: tools}
}

func (c *Client) sign(req *http.Request, body []byte) error {
	access := firstNonEmpty(c.extraString("aws_access_key_id"), os.Getenv("AWS_ACCESS_KEY_ID"))
	secret := firstNonEmpty(c.extraString("aws_secret_access_key"), os.Getenv("AWS_SECRET_ACCESS_KEY"))
	token := firstNonEmpty(c.extraString("aws_session_token"), os.Getenv("AWS_SESSION_TOKEN"))
	if access == "" || secret == "" {
		return fmt.Errorf("amazon-bedrock: set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY")
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	req.Header.Set("X-Amz-Date", amzDate)
	if token != "" {
		req.Header.Set("X-Amz-Security-Token", token)
	}
	hash := sha256.Sum256(body)
	req.Header.Set("X-Amz-Content-Sha256", hex.EncodeToString(hash[:]))
	signedHeaders, canonicalHeaders := canonicalHeaders(req)
	canonicalReq := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		req.URL.RawQuery,
		canonicalHeaders,
		signedHeaders,
		hex.EncodeToString(hash[:]),
	}, "\n")
	scope := date + "/" + c.region() + "/bedrock/aws4_request"
	sum := sha256.Sum256([]byte(canonicalReq))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	signingKey := awsSigningKey(secret, date, c.region(), "bedrock")
	sig := hmacSHA256Hex(signingKey, stringToSign)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+access+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+sig)
	return nil
}

func canonicalHeaders(req *http.Request) (string, string) {
	headers := map[string]string{"host": req.URL.Host}
	for k, vals := range req.Header {
		if len(vals) == 0 {
			continue
		}
		headers[strings.ToLower(k)] = strings.Join(vals, ",")
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(strings.TrimSpace(headers[k]))
		b.WriteByte('\n')
	}
	return strings.Join(keys, ";"), b.String()
}

func awsSigningKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

func hmacSHA256Hex(key []byte, data string) string {
	return hex.EncodeToString(hmacSHA256(key, data))
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) extraString(key string) string {
	if c.cfg.Extra == nil {
		return ""
	}
	v, _ := c.cfg.Extra[key].(string)
	return strings.TrimSpace(v)
}

func contentToString(c any) string {
	switch v := c.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
