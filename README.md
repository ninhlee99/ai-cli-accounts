# ai-cli-accounts

AI CLI account manager. Command: `am`.

Snapshot and swap the local login state of **Claude Code**, **Codex**, and
**Gemini CLI** without logging out and back in. A local proxy also rotates
Claude accounts automatically **before** a rate limit interrupts you.

## Why

Each CLI stores login state (Keychain / a JSON file) for **one account at a
time** — switching means logout, browser, login, every time. `am` snapshots
each account into an encrypted profile so switching is one command, and the
proxy swaps accounts *before* a 429 stops you, mid-session, no restart.

## Install & setup

```sh
curl -fsSL https://raw.githubusercontent.com/ninhlee99/ai-cli-accounts/main/install.sh | sh
```

Clones to a temp dir, builds, installs to `/usr/local/bin`, runs `am setup`
(Claude Code hook + `/am:feedback` slash command), cleans up. Needs macOS,
Go 1.22+, git.

Then:

```sh
claude          # first account is snapshotted automatically
```

For a second account: `/login` in Claude Code, open a new `claude` tab —
it's picked up into rotation on its own, no extra command needed.

**Update:** re-run the install command above — it always builds fresh.
**Uninstall:** `am hook uninstall && rm ~/.claude/commands/am/feedback.md && rm /usr/local/bin/am` (add `rm -rf ~/.am` to also drop saved profiles).

## Commands

| command | what it does |
|---|---|
| `am setup` | hook install + `/am:feedback` slash command, both global |
| `am add [tool] [name]` | snapshot the logged-in account (default tool `claude`, default name = its email) |
| `am ls [tool]` | list profiles — ID, name, account, active, saved date |
| `am sw` / `am sw <id\|name>` | switch account (menu, or straight to it) — no restart |
| `am rename <id\|name> <new>` | give a profile a custom name |
| `am rm <id\|name>` | move a profile to trash (asks first) |
| `am restore <id\|name>` / `--backup` | bring back a trashed profile / re-import the latest auto-backup |
| `am status` | proxy state, sessions attached, effective base URL, per-account state (5h/7d limit usage) |
| `am usage [day\|week\|month\|all]` | tokens used through the proxy, by account and by model (default: week) |
| `am hook install\|uninstall\|status` | wire/remove the Claude Code proxy hook |
| `am proxy` | run the proxy in the foreground (normally automatic; stays up until `am proxy down --force`, which refuses while any claude tab is attached unless you add `--yes-i-know`) |
| `am env` | print `export` lines for the shell rc (`eval "$(am env)"`) — always resolves `ANTHROPIC_BASE_URL` to the proxy |
| `am env set\|get\|rm\|list <key> [value]` | manage extra vars exported alongside it |
| `am export [tool] [name..] [-o file\|--stdout]` | encrypted, passphrase-protected profile blob — file by default (timestamped) |
| `am import [-f file] [--activate tool=name]` | read an export blob back in (merge, never overwrites) |
| `am feedback [-b\|--bug\|-i\|--idea] [title]` | file a GitHub issue |
| `am help` | usage |

`<id|name>` accepts a profile ID (`claude1`), exact name, account email, or
any unique substring of either.

Profiles live in `~/.am/profiles/<tool>/`, AES-256-GCM encrypted under a key
in the macOS Keychain. Nothing is deleted outright — `am rm` trashes, and
every `add`/`rm` auto-backs-up first. Edit `~/.am/config.json` to add
tools/paths.
