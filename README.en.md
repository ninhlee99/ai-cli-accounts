# ai-cli-accounts (`am`)

<p align="center">
  <b>AI CLI Account Manager & Local AI Proxy Gateway</b><br>
  Snapshot, auto-rotate Claude Code accounts, and bridge OpenAI Gateway across Multi-Provider LLMs.
</p>

---

**ai-cli-accounts** (CLI command: `am`) is a production-ready AI CLI Account Manager and Local AI Proxy Gateway written in Golang.

It lets you snapshot and seamlessly swap local login states of **Claude Code**, **Codex**, and **Gemini CLI** without logging out and back in. A background daemon proxy automatically rotates Claude accounts **before** rate limits interrupt your work, and provides a standard **OpenAI-compatible AI Gateway** with intelligent auto-failover across multiple LLM providers.

> 📖 **Architecture:** Read the technical design, DDD structure, and data flows in [STRUCT.md](STRUCT.md).

---

## Key Features

1. **Auto-Rotate Claude Accounts:** Seamlessly rotates through multiple Claude Pro / Max accounts before hitting rate limits (tracked via upstream `anthropic-ratelimit-*` headers) and refreshes OAuth tokens automatically.
2. **Local AI Gateway (`/v1/chat/completions`):** Provides an OpenAI-compatible endpoint (`http://127.0.0.1:8787/v1`) for external developer tools (Python, Node.js, Cursor, Continue, LangChain...).
3. **Multi-Provider Failover Pool:** Automatically routes traffic to fallback providers (GitHub Models, Google AI Studio, Groq, DuckDuckGo AI, Claude Web, ChatGPT Web) upon receiving HTTP 429 with an automatic 30-minute cooldown window.
4. **Context Injection & Stateless Retention:** Preserves multi-turn conversation history across account rotations and provider failovers.
5. **Token Usage Analytics:** Granular tracking of input/output tokens broken down by day, model, project (resolved via client TCP sockets), and session.
6. **Robust Security:** Encrypted credential storage using macOS Keychain and AES-256-GCM / Scrypt key derivation.

---

## Installation & Quick Start

### 1. One-line installer:
```sh
curl -fsSL https://raw.githubusercontent.com/ninhlee99/ai-cli-accounts/main/install.sh | sh
```
This script clones into a temporary directory, builds with Go 1.22+, installs the binary to `/usr/local/bin/am`, configures the Claude Code hook (`am setup`), and cleans up.

### 2. Usage:
```sh
claude          # Your first account is automatically snapshotted into am
```

To add a 2nd account: run `/login` inside Claude Code, then open a new terminal tab — the new account will be auto-detected and added to the rotation pool.

* **Update:** Re-run the install script above.
* **Uninstall:** 
  ```sh
  am hook uninstall
  rm -f ~/.claude/commands/am/feedback.md
  rm -f /usr/local/bin/am
  # Optional: rm -rf ~/.am (to wipe all stored profiles and configuration)
  ```

---

## CLI Command Reference (`am`)

| Category | Command | Description |
| :--- | :--- | :--- |
| **Setup** | `am setup` | Install Claude Code hook + `/am:feedback` slash command |
| **Profiles** | `am add [tool] [name]` | Snapshot current logged-in account into profile (default: `claude`) |
| | `am ls [tool]` | List saved profiles with short IDs (`claude1`, ...), name, email, active mark |
| | `am sw` / `am sw <id\|name>` | Interactive arrow-key menu to switch profile or AI provider |
| | `am current [tool]` | Print currently active account on system |
| | `am rename <id\|name> <new>` | Rename an existing profile |
| | `am rm <id\|name>` | Move a profile to trash |
| | `am restore <id\|name>` | Restore a profile from trash |
| | `am restore --backup` | Restore latest auto-backup |
| **Providers & Gateway** | `am accounts` | List configured providers in pool with priorities and models |
| | `am login [provider]` | Login wizard for Web / API (`chatgpt`, `claude`, `gemini`, `github`, `groq`) |
| | `am api add <name> --endpoint <url> --api-key <key>` | Add an OpenAI-compatible provider to pool |
| | `am api rm <name>` / `am api ls` | Remove or list API providers |
| | `am chat [prompt]` | Interactive terminal chat REPL powered by the provider pool |
| **Monitoring & Ops** | `am status` | Show proxy daemon state, active sessions, 5h/7d quota, and pool health |
| | `am usage [day\|week\|month\|all]` | Token usage summary table (defaults to per-day) |
| | `am usage -D [-d YYYY-MM-DD] [-p PROJECT]` | Detailed token usage by session, project, and model |
| | `am proxy [up\|down]` | Manage background proxy daemon (default `:8787`) |
| | `am env` | Print `export ANTHROPIC_BASE_URL=...` for `eval "$(am env)"` |
| | `am hook [install\|uninstall\|status]` | Manage hooks in `~/.claude/settings.json` |
| | `am export [tool] [name..]` | Export encrypted profile bundle (.amexp) with passphrase |
| | `am import [-f file]` | Import encrypted profile bundle |
| | `am feedback` | File a GitHub issue for bugs or feature ideas |

---

## Using as Local AI Gateway (`http://127.0.0.1:8787`)

The local proxy at `127.0.0.1:8787` acts as a unified OpenAI-compatible Gateway. Point any client or tool to this endpoint to take advantage of auto-rotation and failover:

### 1. Python (Official `openai` SDK)
```python
from openai import OpenAI

client = OpenAI(
    base_url="http://127.0.0.1:8787/v1",
    api_key="am-proxy"  # Any non-empty string
)

# Full Server-Sent Events (SSE) streaming support
stream = client.chat.completions.create(
    model="default",
    messages=[{"role": "user", "content": "Explain goroutines in Go"}],
    stream=True
)
for chunk in stream:
    if chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="", flush=True)
```

### 2. Node.js / TypeScript
```typescript
import OpenAI from "openai";

const openai = new OpenAI({
  baseURL: "http://127.0.0.1:8787/v1",
  apiKey: "am-proxy",
});

const completion = await openai.chat.completions.create({
  model: "default",
  messages: [{ role: "user", content: "Hello from TypeScript!" }],
});
console.log(completion.choices[0].message.content);
```

### 3. Cursor / Continue / Cline / LangChain
Configure in environment variables or IDE settings:
```bash
OPENAI_BASE_URL="http://127.0.0.1:8787/v1"
OPENAI_API_KEY="am-proxy"
```

---

## Multi-Provider Pool Configuration

Providers are configured in `~/.am/accounts.json` (see example at [`accounts.example.json`](accounts.example.json)). You can use `env:VARIABLE_NAME` to resolve secrets securely from environment variables:

```json
{
  "providers": [
    {
      "id": "github-models",
      "type": "openai_compatible",
      "priority": 1,
      "baseUrl": "https://models.github.ai/inference",
      "apiKey": "env:GITHUB_MODELS_TOKEN",
      "model": "gpt-4o"
    },
    {
      "id": "google-ai-studio",
      "type": "openai_compatible",
      "priority": 2,
      "baseUrl": "https://generativelanguage.googleapis.com/v1beta/openai",
      "apiKey": "env:GOOGLE_AI_STUDIO_KEY",
      "model": "gemini-2.0-flash"
    },
    {
      "id": "groq",
      "type": "openai_compatible",
      "priority": 3,
      "baseUrl": "https://api.groq.com/openai/v1",
      "apiKey": "env:GROQ_API_KEY",
      "model": "llama-3.3-70b-versatile"
    },
    {
      "id": "duckduckgo",
      "type": "duckduckgo",
      "priority": 4,
      "model": "claude-3-haiku-20240307"
    }
  ]
}
```

---

## License & Contributing

Licensed under the MIT License. Contributions and feedback are welcome via `am feedback` or GitHub Pull Requests!
