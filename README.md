# ai-cli-accounts

AI CLI account manager. Command: `am`.

Snapshot and swap the local login state of **Claude Code**, **Codex**, and
**Gemini CLI** without logging out and back in. Plus a local proxy that rotates
Claude accounts automatically **before** a rate limit interrupts your work.

## Why

Claude Code, Codex, and Gemini CLI each store login state in one place (macOS
Keychain entry / a JSON file under `~/.config`) — one account at a time.
Running more than one account (personal + work, or several to spread out rate
limits) means logging out, opening a browser, logging back in, every time you
switch — and if you're mid-task when the active account hits its limit, work
stops until you do that dance.

`am` exists to remove that dance:

- **Snapshot instead of re-login.** `am add` copies the credential files +
  keychain entries into an encrypted profile; `am sw` writes a different
  profile back in one command — seconds, no browser.
- **Never lose the terminal you're in.** Switching doesn't touch your running
  shell or the CLI's process — just the credentials it reads.
- **Don't get interrupted by a rate limit.** The proxy sits in front of
  `claude`, watches the account's remaining quota, and swaps to the next
  saved account *before* a 429 would have stopped you — mid-session, no
  restart, nothing redone.
- **Move between machines without re-auth.** `am export` / `am import` carry
  profiles across machines as one passphrase-encrypted blob.

Nothing is deleted outright, everything at rest is encrypted (AES-256-GCM,
key in the Keychain), and it's macOS-native — no daemon babysitting, no
background service beyond what a live Claude Code session needs.

## Install

```sh
git clone <repo>/ai-cli-accounts && cd ai-cli-accounts
go build -o am .
mv am /usr/local/bin/
am setup                     # hook + /am:feedback, installed globally (see below)
```

macOS only (uses the `security` keychain CLI). Needs Go 1.22+.

`am setup` does everything `am hook install` does (see [Auto-rotating
proxy](#auto-rotating-proxy-claude)), plus installs the `/am:feedback` slash
command to `~/.claude/commands/am/` — global, so it works from any project
directory in Claude Code, not just this repo. Safe to re-run after an
update (see below); it only overwrites its own entries.

Verify:

```sh
am help                      # prints usage if the binary is on PATH
```

## Update

```sh
cd ai-cli-accounts
git pull
go build -o am .
mv am /usr/local/bin/
```

Rebuilding overwrites the binary; profiles in `~/.am` are untouched. No need
to re-run `am setup` / `am hook install` after an update — they only wire
shell rc, Claude Code settings, and the slash command once, and those still
point at the same `am` binary. Re-run `am setup` only if you want to pick up
a newer `/am:feedback` command definition.

## Uninstall

```sh
am hook uninstall            # removes the Claude Code hook + ANTHROPIC_BASE_URL
                              # line from your shell rc (open a new shell after)
rm ~/.claude/commands/am/feedback.md   # removes the /am:feedback slash command
rm /usr/local/bin/am
rm -rf ~/.am                 # deletes all saved profiles, backups, trash — optional
```

Run `am hook uninstall` **before** deleting the binary, otherwise Claude
Code's `SessionStart` hook will fail trying to call a missing `am`. Skip
`rm -rf ~/.am` if you plan to reinstall later and want to keep your profiles.

## Commands

| command | what it does |
|---------|--------------|
| `am setup` | one-shot onboarding: `am hook install` + installs `/am:feedback` globally to `~/.claude/commands/am/`. |
| `am add [tool] [name]` | snapshot the account you're currently logged into (default tool: `claude`, default name: the account email). If nothing new is detected, walks you through logging into another account first. |
| `am ls [tool]` | list saved profiles — ID, account, which one is active (`*`), when it was saved. |
| `am sw` / `am switch` | menu picker (↑/↓, Enter) to switch the active account, no restart needed. |
| `am sw <id\|name>` | switch straight to that profile — skip the menu. |
| `am rm <id\|name>` | move a profile to `~/.am/trash/` (asks to confirm first — nothing is deleted outright). |
| `am rename <id\|name> <new>` | give a profile a custom name (default name is the account email) — `am ls` shows it in the NAME column. |
| `am restore <id\|name>` | bring the most recent matching trashed profile back. |
| `am restore --backup` | re-import the latest auto-backup from `~/.am/backups/` (additive, doesn't touch existing profiles). |
| `am status` | active account per tool, current rate-limit state, proxy switch count. |
| `am hook install` | wire Claude Code's `SessionStart`/`SessionEnd` hooks to start/stop the proxy, and add `ANTHROPIC_BASE_URL` to your shell rc. |
| `am hook status` | show whether the hook + env var are currently installed. |
| `am hook uninstall` | remove both. |
| `am proxy` | run the rotating proxy in the foreground yourself (normally started automatically by the hook). |
| `am export [tool] [name..] [-o file\|--stdout]` | write an encrypted, passphrase-protected blob of profiles to a timestamped file (or `-o file`, or `--stdout` to print it). |
| `am import [-f file] [--activate tool=name]` | read that blob back in (merges — never overwrites an existing profile) and optionally switch to one of the imported accounts. |
| `am feedback [-b\|--bug\|-i\|--idea] [title]` | file a GitHub issue — prompts for title/body, then uses `gh` if installed, else opens a prefilled issue URL in the browser. |
| `am help` | print built-in usage. |

`<id|name>` accepts a profile ID (`claude1`), an exact profile name, the
account's email, or any unique substring of either — `am sw goog` matches a
profile whose email contains "goog" as long as it's the only match.

## Profiles

```sh
am add                       # save whatever account you're logged into now
                             # (log into another one, run again to add it)
am add work                  # ...named "work" instead of the account email
am add codex                 # same for codex / gemini
am add codex work            # tool + custom name together
am ls                        # list profiles with their IDs, * = active
am sw                        # pick an account from a menu (↑/↓, Enter)
am sw claude2                # ...or switch straight to it by ID
am rm claude2
am rename claude2 work       # rename after the fact, if you didn't at `am add` time
am status                    # active account + rate limits
```

Each profile gets a short ID — `claude1`, `claude2`, `codex1` — assigned by
age (oldest = 1). `am sw` / `am rm` / `am rename` take that ID, the account
email, an exact profile name, or any unique part of the name/email. A
profile's name defaults to its account email; give `am add` a trailing word
that isn't a tool name to use that instead (`am add work`, `am add codex
work`) — or rename it later with `am rename`. Either way the account itself
doesn't change, just the label `am ls`/`am sw`/`am export` show.

Profiles live in `~/.am/profiles/<tool>/<name>.amp`, encrypted with AES-256-GCM
under a key stored in the macOS Keychain (`am-master-key`). The `*.meta.json`
sidecar next to each is cleartext but holds only the email and a timestamp.

### Nothing is deleted outright

- `am rm` asks to confirm, then **moves** the profile to `~/.am/trash/`.
  `am restore <id|name>` brings the most recent match back.
- Every `am add` (and every `am rm`) first writes an encrypted snapshot of
  **all** profiles to `~/.am/backups/` (keyed by the machine's master key, 20
  kept). `am restore --backup` re-imports the latest — additive, existing
  profiles untouched.

## Move accounts to another machine

No re-login needed. On the source machine:

```sh
am export                    # all profiles -> ./all-accounts-<timestamp>.amexp
am export claude             # just Claude's -> ./claude-<timestamp>.amexp
am export claude ninhle      # one profile -> ./claude-ninhle-<timestamp>.amexp
am export -o blob.txt        # write to a specific file instead
am export --stdout           # print the AMEXP1.… line instead of writing a file
```

Prompts for a passphrase (twice), then writes the encrypted blob to a file —
by default a timestamped name in the current directory, so a repeated export
never overwrites an earlier one; `-o <file>` picks the name yourself.
`--stdout` skips the file and prints the `AMEXP1.<base64>` line instead (the
old default) — useful for piping, but that line is your encrypted secret in
your terminal history/scrollback, so prefer the file unless you need stdout.

The blob is long because it *is* the encrypted secret, not a wrapper around
one — salt(16B) + nonce(12B) + your OAuth tokens/cookies gzip'd and encrypted
+ a 16B auth tag, all base64. There's no shorter form that stays this
self-contained: shrinking it further would mean either weaker encryption or
handing you a reference to a secret stored somewhere else instead of the
secret itself. Writing it to a file (the default) keeps it off your
clipboard/terminal history entirely.

On the target machine:

```sh
am import                    # paste the AMEXP1.… line, then Ctrl-D
am import -f blob.txt         # ...or read it from the file `am export -o` made
am import --activate claude=you@gmail.com   # also `am switch` to it after import
```

Merge rules:

- a profile whose **tool + account** already exists here → **kept as-is**
  (its existing token is *not* overwritten by the imported one);
- a profile whose **name** is already taken by a different account → the
  existing one keeps the name; the import is added under a new name
  (`<name>-<account>`, else `<name>-2`, `-3`, …);
- everything else → added.

Import only writes to `~/.am`. It does not touch the live keychain / config
files until you `am switch` (or pass `--activate`). Pre-existing profiles always
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
am hook install              # wires Claude Code's start/stop hooks + adds
                             # ANTHROPIC_BASE_URL to your shell rc
# open a new shell, then:
claude                       # first account is snapshotted automatically
```

For a second (or third…) account: `/login` in Claude Code to switch, open a
new `claude` tab — that account is snapshotted and added to rotation on its
own, no `am add` needed. (`am add` still works if you'd rather do it by
hand, or want to add a codex/gemini profile — those aren't proxy-rotated so
they don't get auto-added.)

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
- Every `SessionStart` (every `claude` launch, not just the one that spawned
  the proxy) syncs against whatever's actually logged in: if that account has
  no profile yet, it's snapshotted and pulled into rotation automatically —
  no `am add` needed. Log into a second account in `claude` → `/login`, open
  a new `claude` tab, and it's already in rotation.
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
- Codex / Gemini have no proxy rotation. `am add` / `am switch` still work;
  `am switch` there writes the credential to disk, so restart the tool.

## Feedback

```sh
am feedback                  # prompts for title + body, files a GitHub issue
am feedback -i "shorter export blob"   # --idea, title given up front
am feedback -b "am sw crashes on empty profile list"   # --bug (default)
```

Uses `gh issue create` when the `gh` CLI is installed and logged in;
otherwise opens a prefilled `github.com/.../issues/new` URL in your browser.
Every report includes OS/arch and the active Claude profile automatically —
no need to type that part.

`am setup` (or `am hook install` alone if you'd rather skip it) installs
`/am:feedback [title]` globally to `~/.claude/commands/am/` — it runs the
same command, and works from Claude Code in any project, not just this repo.

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
| `feedback.go`         | `am feedback` — files a GitHub issue via `gh` or browser |
| `setup.go`            | `am setup` — hook install + global slash-command install |
| `commands/feedback.md` | source for `/am:feedback`, embedded into the binary at build time |
| `.claude/commands/am/feedback.md` | same file, kept here too so `/am:feedback` also works when Claude Code runs inside this repo pre-install |
