# Integrations

This page lists the external services mivia can contact. Your files stay on
your machine by default. mivia contacts an external service only for the
paths below, and only when you configure them. Review each setting before
use.

## AI providers

mivia sends prompts and selected context to one configured AI provider. Eight
providers are built in today:

| Provider | Default model | Default API base URL |
|----------|---------------|-----------------------|
| DeepSeek (default) | `deepseek-v4-flash` | `https://api.deepseek.com/v1` |
| Anthropic | `claude-sonnet-5` | `https://api.anthropic.com/v1` |
| OpenRouter | `openai/gpt-5.6-luna` | `https://openrouter.ai/api/v1` |
| ZAI (z.ai) | `glm-5.2` | `https://api.z.ai/api/paas/v4` |
| Ollama | `gpt-oss:120b` | `https://ollama.com/v1` |
| LLM Gateway | `deepseek-v4-pro` | `https://api.llmgateway.io/v1` |
| LLM Proxy CLI | `claude-sonnet-5` | `http://127.0.0.1:8317/v1` |
| MiniMax | `MiniMax-M3` | `https://api.minimax.io/v1` |

Every provider ships with the default model shown above. Selecting a
provider under `[providers.<name>]` in `mivia.toml` is what activates it;
until then, DeepSeek is the active default. mivia does not accept an
arbitrary OpenAI-compatible provider name. See
[Configuration](config.md#provider-support) for the full provider list, key
setup, and the model catalog rule.

## Web search

The `search` tool calls the Tavily API when you set `TAVILY_API_KEY`. Without
a key, it falls back to free web search engines (DuckDuckGo, Bing) and skips
the Tavily-only options. See
[Configuration](config.md#web-research-response-bound) for the response size
bound.

## MCP servers

mivia can connect to Model Context Protocol (MCP) servers over stdio or
Streamable HTTP. A project or user settings file declares each server; an
agent must be allowed to select it. See
[Configuration](config.md#mcp-servers) for setup, scoping, and the fail-closed
connection rule.

## Lifecycle hooks

mivia can run your own local scripts on `PreToolUse`, `PostToolUse`, and
`Stop` events. A hook can reach any external service your script contacts.
See [Lifecycle hooks](../development/lifecycle-hooks.md) for the wire
protocol and trust model.

## Workflow delivery

A workflow run can publish its result to an external system (for example, a
pull request) once you pass the explicit `--allow-publish` flag. See the
[Workflow guide](workflows-guide.md) for the delivery step and its gates.

## See also

- [Configuration](config.md) - provider keys, MCP servers, tool policy
- [Security and privacy](../security/overview.md) - data handling
