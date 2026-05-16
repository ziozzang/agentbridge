# Installation

The harness is a single static Go binary.

## Prerequisites

- Go 1.21+
- A POSIX-like environment (Linux, macOS, WSL); Windows works for
  development but plugin tests rely on `os.Symlink` etc.

## Build from source

```bash
git clone https://github.com/ziozzang/glm-acp
cd glm-acp
go build -o glm-acp-agent ./cmd/glm-acp-agent
```

The binary is self-contained — no runtime dependencies, no shared libraries.

## Container

A multi-stage `Dockerfile` ships in the repo root:

```bash
docker build -t acp-harness .
docker run --rm -i \
  -e ACP_HARNESS_API_KEY="$OPENAI_API_KEY" \
  -e ACP_HARNESS_PROVIDER=openai \
  -v "$PWD":/workspace -w /workspace \
  acp-harness
```

Mount a volume at `/home/agent/.local/state` to persist sessions across
container restarts.

## Editor integration

Any ACP-aware editor can spawn the harness. The most common is **Zed**:
configure your assistant settings to invoke `glm-acp-agent` as the agent
command. See [docs/providers.md](./providers.md) for how to point it at a
specific upstream LLM.

## First-time setup

```bash
# Pick a provider via env (default is "glm" for back-compat).
export ACP_HARNESS_PROVIDER=openai
export ACP_HARNESS_API_KEY=sk-...

# Or store a key in the credentials file (XDG-aware, mode 0600).
./glm-acp-agent --setup
```

`--setup` stores a key at `$XDG_CONFIG_HOME/glm-acp-agent/credentials.json`
(or `~/.config/glm-acp-agent/credentials.json`) and is back-compat with
the original GLM tool — handy when you only need GLM. For non-GLM
providers, prefer `ACP_HARNESS_*_API_KEY` env vars.
