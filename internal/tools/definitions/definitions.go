// Package definitions holds the OpenAI function-calling JSON schemas we
// expose to the GLM model.
package definitions

import "encoding/json"

// Tool is the OpenAI function-calling definition.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction is the function schema portion of a Tool.
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// All returns the full set of agent-owned tool definitions.
func All() []Tool { return append([]Tool(nil), allTools...) }

// ByName returns the tool with the given name, or nil if not found.
func ByName(name string) *Tool {
	for i := range allTools {
		if allTools[i].Function.Name == name {
			t := allTools[i]
			return &t
		}
	}
	return nil
}

// Filter returns the subset of tools whose names appear in keep, in the
// canonical declaration order.
func Filter(keep []string) []Tool {
	set := make(map[string]struct{}, len(keep))
	for _, k := range keep {
		set[k] = struct{}{}
	}
	out := make([]Tool, 0, len(keep))
	for _, t := range allTools {
		if _, ok := set[t.Function.Name]; ok {
			out = append(out, t)
		}
	}
	return out
}

var allTools = []Tool{
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "read_file",
			Description: "Read the text content of a file from the agent process. Relative paths resolve against the ACP session working directory.",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Absolute or relative path to the file to read."}
  },
  "required": ["path"]
}`),
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "write_file",
			Description: "Write or overwrite a text file from the agent process after asking the user for permission. Relative paths resolve against the ACP session working directory.",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Absolute or relative path to the file to write."},
    "content": {"type": "string", "description": "The full text content to write to the file."}
  },
  "required": ["path", "content"]
}`),
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "list_files",
			Description: "List the files and directories at the given path from the agent process. Relative paths resolve against the ACP session working directory.",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "Absolute or relative path of the directory to list."}
  },
  "required": ["path"]
}`),
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "run_command",
			Description: "Execute a shell command via 'sh -c' in the ACP session working directory and return stdout, stderr, and exit code. The user is asked for permission before each invocation.",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "command": {"type": "string", "description": "The shell command line to execute (interpreted by 'sh -c', so quoting, pipes, and redirects all work)."}
  },
  "required": ["command"]
}`),
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "web_search",
			Description: "Search the web using Z.AI's premium search engine and return relevant results, including titles, URLs, sources, dates, and content summaries.",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "The search query."},
    "count": {"type": "integer", "description": "Number of results to return (1-50). Default is 10.", "minimum": 1, "maximum": 50}
  },
  "required": ["query"]
}`),
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "web_reader",
			Description: "Fetch and parse the content of a web page at the given URL via Z.AI's reader, returning the main text content as markdown or plain text.",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "url": {"type": "string", "description": "The URL of the page to read."},
    "return_format": {"type": "string", "description": "Return format: 'markdown' (default) or 'text'.", "enum": ["markdown", "text"]}
  },
  "required": ["url"]
}`),
		},
	},
	{
		Type: "function",
		Function: ToolFunction{
			Name:        "image_analysis",
			Description: "Analyze an image (local file path or remote URL) using Z.AI Coding Plan Vision MCP. Returns a textual description / answer. Use this to extract text from screenshots, describe diagrams, or answer questions about images the user has referenced.",
			Parameters: json.RawMessage(`{
  "type": "object",
  "properties": {
    "image_source": {"type": "string", "description": "Local file path or remote URL of the image to analyze."},
    "prompt": {"type": "string", "description": "Optional question or instruction guiding the analysis. Defaults to a general description."}
  },
  "required": ["image_source"]
}`),
		},
	},
}
