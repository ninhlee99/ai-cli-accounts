---
description: File a GitHub issue for am (bug or idea) via `am feedback`
argument-hint: [-b|--bug|-i|--idea] [title]
---

Run `am feedback $ARGUMENTS` in the shell.

`am feedback` prompts (in the terminal) for a title if none was given, then a
multi-line body ended by a blank line, then either:
- shells out to `gh issue create -R ninhlee99/amux` if `gh` is
  installed and authenticated, or
- opens a prefilled `github.com/.../issues/new?...` URL in the browser.

Do not fabricate the title or body yourself — this command is interactive by
design so the person filing the issue writes it in their own words. Just run
the command and let its prompts happen in the terminal; relay whatever it
prints (the issue URL, or the "opening: <url>" line) back once it finishes.
