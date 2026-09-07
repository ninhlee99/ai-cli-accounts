# ai-cli-accounts

AI CLI account manager. Command: `am`.

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
git clone <repo>/ai-cli-accounts && cd ai-cli-accounts
go build -o am .
mv am /usr/local/bin/
```

macOS only (uses the `security` keychain CLI). Needs Go 1.22+.

## Profile management

```sh
am current                   # who each tool is logged in as right now

am save claude               # snapshot current login; profile name = the
                             # account email, e.g. "you@gmail.com"
am save claude work          # ...or give it your own name
am ls                        # list profiles + their account, * = active

am use claude you@gmail.com  # restore a profile on disk
am use claude you            # partial name is fine if it's unambiguous
am switch claude work        # switch account now — live via the proxy, no restart
am rm claude work
```

`am save` reads the logged-in account from each CLI's own token (Claude/Codex/
Gemini all expose an email), so you rarely have to name profiles yourself.
`am use` / `am switch` / `am rm` accept an exact name, or a unique substring of
the name or the account email.

`am use` auto-snapshots the current (unsaved) login as `_prev` first, so nothing
is ever lost.

Profiles live in `~/.am/profiles/<tool>/<name>.amp`, encrypted with AES-256-GCM.
The master key is generated once and stored in the macOS Keychain
(`am-master-key`). The cleartext `*.meta.json` sidecar holds only the account
name and timestamp — no secrets.

## Move accounts to another machine

No re-login needed. On the source machine:

```sh
am export                    # all profiles; prompts for a passphrase (twice)
am export claude             # just Claude's profiles
am export claude you@gmail.com other@x.com   # specific ones
```

It prints one line: `AMEXP1.<base64>` — a gzip'd JSON of the profiles,
encrypted with AES-256-GCM under a key derived from your passphrase
(scrypt N=2^15). Safe to paste into chat or a note; useless without the
passphrase.

On the target machine:

```sh
am import                    # paste the AMEXP1.… line, then Ctrl-D
am import -f blob.txt         # ...or read it from a file
am import --activate claude=you@gmail.com   # also `am use` it after import
```

Merge rules:

- a profile whose **tool + account** already exists here → **kept as-is**
  (its existing token is *not* overwritten by the imported one);
- a profile whose **name** is already taken by a different account → the
  existing one keeps the name; the import is added under a new name
  (`<name>-<account>`, else `<name>-2`, `-3`, …);
- everything else → added.

Import only writes to `~/.am`. It does not touch the live keychain / config
files until you `am use` (or pass `--activate`). Pre-existing profiles always
remain in the list.

Non-interactive: set `AM_PASSPHRASE` instead of being prompted.

### What gets captured

| tool   | artifacts |
|--------|-----------|
| claude | keychain `Claude Code-credentials` (raw JSON, preserved byte-for-byte) + `~/.claude.json` + `~/.claude/.credentials.json` |
| codex  | `~/.codex/auth.json` |
| gemini | `~/.gemini/oauth_creds.json`, `google_accounts.json`, `installation_id` |

Edit `~/.am/config.json` to add paths or tools.

## Auto-rotating proxy (Claude)

Set it up once, then use plain `claude`. Nothing changes about how you launch
it — no wrapper.

```sh
am save claude               # snapshot each account (log into the next one
am save claude               #   in Claude, run again) — 2+ for rotation
am hook install              # wires Claude Code's start/stop hooks + adds
                             # ANTHROPIC_BASE_URL to your shell rc
# open a new shell, then:
claude
```

The proxy is **not a daemon**. Claude Code's `SessionStart` hook starts it
(the first tab that needs it), `SessionEnd` releases it, and it **stops
itself ~30s after the last tab closes**. Multiple tabs are ref-counted — as
long as one Claude session is open, the proxy stays up. If a `claude` process
dies without its hook firing, the proxy notices the silence and stops within
30 minutes.

When the active account nears its limit (or gets a 429), the proxy installs
the next account's credential onto the system and points itself there. The
running `claude` keeps going and picks up the new account on its next
keychain read — no restart. `/usage` reports the account in use, because
every request (that one included) carries its token.

```sh
am status                    # active account, limits, switch count
am switch claude <name>      # force a switch now, no restart
am hook status|uninstall
am proxy                     # run it in the foreground yourself (rarely needed)
```

Only `ANTHROPIC_BASE_URL` is set — **not** `ANTHROPIC_AUTH_TOKEN`. That keeps
Claude Code in OAuth mode (account shows in the UI, subscription billing, not
API credit); requests just travel through the proxy.

How it works:

- **Claude Code owns the OAuth refresh.** Refresh tokens rotate on use, so the
  proxy never refreshes — it reads the live keychain token on each request and
  forwards that.
- On startup the proxy adopts the account Claude is logged in as (snapshotting
  it as a profile if new).
- It watches `anthropic-ratelimit-unified-*` headers and **HTTP 429**. Near the
  limit (or on a 429) it installs the next profile's credential and points
  itself there. In-flight request still completes; the next one is the new
  account.
- A limited account goes on cooldown until its reset time, then rotates back.

With `ANTHROPIC_BASE_URL` unset, `claude` talks straight to Anthropic and none
of this applies.

### Limits / honesty

- Anthropic often sends only the window **reset time**, not a live "percent
  remaining", on ordinary calls. Rotation then leans on the 429 fallback:
  reactive — one throttled response before the switch, but the session doesn't
  stop and no work is redone.
- The switch isn't instantaneous end-to-end: the proxy is correct immediately,
  but Claude Code's UI/account view only catches up when it next re-reads the
  keychain.
- The account view in Claude Code still says "API Usage Billing" is **not**
  expected here — if you see it, `ANTHROPIC_AUTH_TOKEN` is set in your
  environment; unset it.
- Codex / Gemini have no proxy rotation (profile save / use / switch work).

## Layout

| file                  | role |
|-----------------------|------|
| `main.go`             | CLI dispatch |
| `config.go`           | tool/artifact definitions, `~/.am/config.json` |
| `profile.go`          | capture / apply / bundle (tar.gz) |
| `crypto.go`           | AES-GCM, master key in keychain |
| `keychain_darwin.go`  | `security` CLI wrapper (raw-value safe) |
| `proxy.go`            | reverse proxy, rotator, auto start/stop lifecycle |
| `claude_token.go`     | Claude OAuth token: read from keychain / bundle |
| `hook.go`             | `am hook` — Claude Code SessionStart/End wiring |
| `transfer.go`         | `am export` / `am import` (passphrase-encrypted bundle) |
