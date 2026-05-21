# Safety Pipeline

AgentBridge is absorbing the safety and request-mutation mechanisms from
`gollm`. These features are separated from provider configuration because they
should apply consistently across ACP, A2A, OpenAI-compatible HTTP,
Anthropic-compatible HTTP, and future protocol surfaces.

## Intent

The target request lifecycle is:

1. Apply declarative inject rules.
2. Drop unsupported or unwanted top-level request parameters.
3. Detect and optionally mask PII before the upstream call.
4. Dispatch to the selected provider or router route.
5. Restore PII placeholders in the response.
6. Strip thinking tags or structured reasoning fields when configured.
7. Cache eligible non-streaming responses only when no PII was detected.

This order matters. PII masking must happen before upstream dispatch; response
rollback must happen before final response sanitization; response caching must
avoid PII-detected requests.

## Current Status

| Area | Status | Implemented part |
| --- | --- | --- |
| PII detection/masking | Wired | `internal/provider/pipeline` masks provider messages before dispatch, restores placeholders on streamed responses, supports reject/route settings, and preserves native compaction through the wrapper. |
| Thinking tag sanitize | Wired | `internal/provider/pipeline` strips `<think>`, `<thinking>`, `<reasoning>`, and `<reflection>` spans from streamed text when enabled. |
| Response cache | Primitive present | `internal/responsecache` provides an in-memory TTL cache with stable SHA-256 JSON keys and stats. |
| Config schema | Present | `pii`, `sanitize`, `cache`, and `inject` are parsed from `config.yaml` by `runtimeconfig`. |
| Parameter drop | Schema present through inject | `inject[].remove` represents top-level parameter drop. Automatic application is pending. |
| JSON mode | Provider-level workaround present | OpenAI-compatible providers can receive `response_format` through provider `extra.request_defaults`; top-level `inject[].set.response_format` is parsed but not yet wired globally. |
| Header changes | Provider static headers present | Provider `headers:` are already sent upstream. Kong-style add/set/remove/rename/replace transforms are still pending. |
| Request path wiring | Provider wrapper active | Configured ACP agent and HTTP/A2A provider construction wraps the active provider, so shared `StreamChat` and native compaction paths receive the safety pipeline. |
| `/v1/providers/status` | Mounted | Read-only operator snapshot with active provider info, in-flight HTTP requests, active ACP sessions, and completed/failed HTTP counters. |

## Configuration

Place these settings in `$XDG_CONFIG_HOME/agentbridge/config.yaml` or a file
specified by `AGENTBRIDGE_CONFIG_FILE`.

```yaml
pii:
  enabled: true
  mask: true
  disable_defaults: false
  env:
    file: ~/env
    min_length: 12
    mask: '[MASK_ENV_SECRET_{n}]'
  routing:
    reject: false
    reject_message: "PII detected"
    route_to: local-private-model
  patterns:
    - name: account_id
      regex: '\bACCT-[0-9]{8}\b'
      mask: '[MASK_ACCOUNT_{n}]'

sanitize:
  strip_think_tags: true
  tags: [think, thinking, reasoning, reflection]

cache:
  enabled: true
  ttl: 1h
  max_size: 10000
  models_to_cache: [gpt-*, claude-*]

inject:
  - when: "grok-*, glm-*"
    system_prompt: "Return concise operational answers."
    system_prompt_mode: prepend
    user_suffix: "\n\nReturn only the final answer."
    remove: [logprobs, top_logprobs]
    request_regex:
      - pattern: '\bSECRET:\s*\S+'
        replace: 'SECRET: [redacted]'
        roles: [user]
```

## PII Detection

Default patterns cover:

- Korean resident registration numbers
- Korean mobile and landline phone numbers
- Email addresses
- Credit-card-like numbers
- IPv4 addresses
- JWT-looking tokens
- Common API key/token shapes such as `sk-*`, `sk-ant-*`, and GitHub `gh*_`
  tokens

Custom `pii.patterns` extend the defaults. A custom pattern with the same
`name` replaces that default. `mask` uses `{n}` as the placeholder counter,
and AgentBridge adds a per-request salt so placeholders do not collide with
normal user text.

`pii.env.file` adds exact-match masking for secrets read from an env dictionary
file. Setting `file` is enough to enable this source. The parser accepts
`KEY=value` and `export KEY=value` lines, optional single/double quotes, and
inline comments outside quotes. By default, every value at least `min_length`
characters long is treated as a secret. Set `names` only when you intentionally
want to narrow loading to specific variables.

`pii.mask: false` is intended for detect-only mode. In that mode routing or
rejection can still happen, but the prompt is not changed before upstream
dispatch. `pii.routing.reject` should take precedence over `route_to` once the
request path is wired.

## Message Sanitization

`sanitize.strip_think_tags` removes model-visible chain-of-thought tags from
assistant text. The default tag catalog is:

- `think`
- `thinking`
- `reasoning`
- `reflection`

The streaming stripper keeps a tail buffer so tags split across SSE chunks can
still be removed. This is separate from provider-native structured reasoning:
thinking deltas can be discarded or omitted at the protocol adapter layer once
the setting is wired in.

## Inject Rules

Inject rules are declarative request edits matched by model name. `when`
accepts exact names, `*` wildcards, or comma-separated patterns.

Supported rule fields:

| Field | Purpose |
| --- | --- |
| `set` | Overlay top-level request fields. |
| `prepend_messages` | Insert messages before the client-provided history. |
| `system_prompt` | Add or edit the first system message. |
| `system_prompt_mode` | `prepend`, `append`, or `replace`. |
| `user_prefix` | Prefix the first user message. |
| `user_suffix` | Suffix the last user message. |
| `remove` | Drop top-level request fields. |
| `request_regex` | Apply regex replacements to message text, optionally by role. |

This is meant for request normalization, unsupported-parameter removal, and
small policy prompts. Keep large agent prompts in agent profiles instead.

## JSON Mode

There are two related mechanisms:

- Provider-level JSON mode: for OpenAI Chat Completions compatible providers,
  use `providers.<name>.extra.request_defaults.response_format`.
- Pipeline-level JSON mode: once `inject` is wired globally, use
  `inject[].set.response_format` to force JSON on selected public model names.

Provider-level example:

```yaml
providers:
  openrouter:
    kind: openai-chat
    extra:
      request_defaults:
        response_format:
          type: json_object
```

Pipeline-level target example:

```yaml
inject:
  - when: "json-*"
    set:
      response_format:
        type: json_object
```

JSON validation is not enforced yet. The current behavior is request shaping,
not post-response validation.

## Header Changes

Static provider headers are already supported:

```yaml
providers:
  openrouter:
    headers:
      HTTP-Referer: https://example.com
      X-Title: AgentBridge
```

The target transform model is intentionally more expressive and should mirror
`gollm`'s actions:

| Action | Purpose |
| --- | --- |
| `add` | Append a header value. |
| `set` | Replace a header value. |
| `remove` | Delete a header. |
| `rename` | Move values from one header name to another. |
| `replace` | Replace matching header values. |

This transform layer is pending. Until then, use static provider `headers:`
for upstream headers and avoid relying on dynamic downstream rewrites.

## Response Cache

The response cache is intentionally small and deterministic:

- It is in-memory only.
- Keys are SHA-256 hashes of canonical JSON payloads plus a namespace.
- TTL expiration is lazy on read.
- `max_size` evicts entries with the earliest expiry first.
- Streaming responses should bypass the cache.
- Tool-using or PII-detected requests should bypass the cache.

The cache is meant to reduce duplicate upstream calls for identical,
non-sensitive, non-streaming requests. It is not a durable store.

## Provider Status

`/v1/providers/status` is now mounted as the operator-facing read-only status
view for this pipeline. The current response includes:

- provider name, kind, base URL, default model, and whether it uses a
  provider-native agent loop
- active in-flight HTTP requests
- active in-memory ACP sessions
- completed and failed HTTP request counters

`/ui/` renders the same snapshot as a simple dashboard for live inspection.

Useful follow-up additions are:

- provider health state and rate-limit state
- request success/error rate and recent latency estimates
- quota state: remaining requests/tokens, reset time, last quota reason
- response cache stats

## Rollout Notes

The common provider wrapper now applies the core safety path for configured
ACP, HTTP, and A2A provider calls. Remaining rollout work is focused on
request inject/drop, dynamic header transforms, response cache policy, and
provider status reporting.

When wiring, preserve this invariant:

```text
request inject/drop -> PII mask -> provider call -> PII unmask -> sanitize -> cache/store
```

Do not store masked requests in response state or response cache unless the
storage policy is explicitly reviewed.
