# am — AI CLI account manager

Snapshot and swap the local login state of **Claude Code**, **Codex**, and
**Gemini CLI** without logging out and back in. Plus a local proxy that rotates
Claude accounts automatically **before** a rate limit interrupts your work.

## Why

Switching accounts normally means: logout → browser → login → lose your place.
`am` snapshots each CLI's credential files + keychain entries into an encrypted
profile, and restores them in one command. The proxy goes further: your running
`claude` never has to stop — when one account nears its limit, the proxy serves
the next request from another account.

## Install

```sh
go build -o am .
mv am /usr/local/bin/
```

macOS only (uses the `security` keychain CLI). Needs Go 1.22+.

## Profile management

```sh
am now                       # who each tool is logged in as right now
am save claude work          # snapshot current Claude login -> profile "work"
am save claude personal      # (switch account in Claude's own flow first)
am ls                        # list profiles, * marks the active one
am use claude personal       # restore that profile
am rm claude work
```

`am use` auto-snapshots the current (unsaved) login as `_prev` first, so nothing
is ever lost.

Profiles live in `~/.am/profiles/<tool>/<name>.amp`, encrypted with AES-256-GCM.
The master key is generated once and stored in the macOS Keychain
(`am-master-key`). The cleartext `*.meta.json` sidecar holds only the account
name and timestamp — no secrets.

### What gets captured

| tool   | artifacts |
|--------|-----------|
| claude | keychain `Claude Code-credentials` (raw JSON, preserved byte-for-byte) + `~/.claude.json` + `~/.claude/.credentials.json` |
| codex  | `~/.codex/auth.json` |
| gemini | `~/.gemini/oauth_creds.json`, `google_accounts.json`, `installation_id` |

Edit `~/.am/config.json` to add paths or tools.

## Auto-rotating proxy (Claude)

Requires **2+ saved Claude profiles**.

```sh
am proxy                     # runs on 127.0.0.1:8787
# in another shell:
am run claude                # execs `claude` with env pointed at the proxy
#   ...or set it yourself:
export ANTHROPIC_BASE_URL=http://127.0.0.1:8787
export ANTHROPIC_AUTH_TOKEN=am-proxy
claude
```

The proxy:

- injects the active profile's OAuth **access token** on every request;
- **refreshes** that token ~2 min before it expires (using the profile's
  refresh token) and writes the fresh token back into the encrypted profile;
- watches `anthropic-ratelimit-unified-*` response headers and **HTTP 429**;
  when the active account is near its limit or gets a 429, the *next* request
  is served from the next profile in rotation — the in-flight request still
  completes, so `claude` never stalls mid-task;
- puts a limited account on cooldown until its reset time, then rotates back.

`GET http://127.0.0.1:8787/_am/status` shows per-account remaining, token
expiry, cooldowns, and switch count.

### Limits / honesty

- Anthropic does not always send a live "percent remaining" header on ordinary
  API calls — often only the window reset time. Rotation then relies on the 429
  fallback (reactive, one throttled response before the switch). Set
  `--upstream` to a metering proxy if you want fully proactive rotation.
- A `claude` process that was started *without* the proxy keeps its old token
  until restart. Start it via `am run claude` (or with the env vars) to get
  hot rotation. `am use` alone only changes what the *next* `claude` start
  picks up — resume your session with `claude --continue`.
- Codex / Gemini proxy rotation is not implemented yet (profile save/use works).

## Layout

| file                  | role |
|-----------------------|------|
| `main.go`             | CLI dispatch |
| `config.go`           | tool/artifact definitions, `~/.am/config.json` |
| `profile.go`          | capture / apply / bundle (tar.gz) |
| `crypto.go`           | AES-GCM, master key in keychain |
| `keychain_darwin.go`  | `security` CLI wrapper (raw-value safe) |
| `proxy.go`            | reverse proxy + rotator |
| `claude_token.go`     | Claude OAuth token parse / refresh |
