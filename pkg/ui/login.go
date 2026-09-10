package ui

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"amux-accounts/pkg/browser"
	"amux-accounts/pkg/provider"
	"amux-accounts/pkg/proxy"
	"amux-accounts/pkg/types"
)

// loginFlags holds optional non-interactive credentials passed on the CLI.
type loginFlags struct {
	model      string
	token      string // access token / API key / sessionKey
	cookie     string // raw Cookie header or name=value
	refresh    string // refresh token when the web session exposes one
	useBrowser bool   // open Chromium via CDP and capture cookie (default when no token/cookie)
	noBrowser  bool
}

func parseLoginFlags(args []string) (providerName string, f loginFlags, rest []string) {
	if len(args) == 0 {
		return "", f, nil
	}
	providerName = strings.ToLower(args[0])
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--model":
			if i+1 < len(args) {
				f.model = args[i+1]
				i++
			}
		case "--token", "--access-token", "--api-key":
			if i+1 < len(args) {
				f.token = args[i+1]
				i++
			}
		case "--cookie":
			if i+1 < len(args) {
				f.cookie = args[i+1]
				i++
			}
		case "--refresh", "--refresh-token":
			if i+1 < len(args) {
				f.refresh = args[i+1]
				i++
			}
		case "--browser":
			f.useBrowser = true
		case "--no-browser":
			f.noBrowser = true
		default:
			rest = append(rest, args[i])
		}
	}
	return providerName, f, rest
}

// CmdLogin handles login. Default for chatgpt/claude: open a dedicated browser
// window, you sign in on the website, we capture the session cookie via CDP
// (no Keychain). Pass --token/--cookie to skip the browser, or --no-browser
// to paste manually.
func CmdLogin(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: amux login <provider> [--browser] [--token T] [--cookie C] [--refresh R] [--model M]")
		fmt.Println("Providers: chatgpt, claude, gemini, gemini-web, github, groq")
		fmt.Println()
		fmt.Println("  chatgpt / claude / gemini-web  default = open browser, CDP cookie capture")
		fmt.Println("  gemini (API)                   AI Studio API key")
		fmt.Println("  --token/--cookie  skip browser, use pasted credentials")
		fmt.Println("  --no-browser      paste interactively instead of opening a window")
		return
	}

	target, flags, _ := parseLoginFlags(args)
	switch target {
	case "chatgpt", "chatgpt-web", "chatgptweb":
		loginChatGPT(flags)
	case "claude", "claude-web", "claudeweb":
		loginClaude(flags)
	case "gemini-web", "geminiweb":
		loginGeminiWeb(flags)
	case "gemini", "google-ai-studio", "geminiapi":
		loginGemini(flags)
	case "github", "github-models":
		loginGitHubModels(flags)
	case "groq":
		loginGroq(flags)
	default:
		fmt.Printf("Unknown provider %q. Supported: chatgpt, claude, gemini, gemini-web, github, groq\n", target)
	}
}

func readLinePrompt(prompt string) string {
	fmt.Print(prompt)
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

func nextPoolID(prefix string) (id string, priorityFloor int, hasExisting bool) {
	f, _ := provider.LoadConfigFile(provider.DefaultAccountsPath())
	n := 0
	maxPriority := 0
	if f != nil {
		for _, p := range f.Providers {
			if pre, num, ok := types.ParseID(p.ID); ok && pre == prefix {
				if num > n {
					n = num
				}
				hasExisting = true
				if p.Priority > maxPriority {
					maxPriority = p.Priority
				}
			}
		}
	}
	return types.FormatID(prefix, n+1), maxPriority + 1, hasExisting
}

func loginChatGPT(f loginFlags) {
	fmt.Println("== Login: ChatGPT Web ==")

	sessionCookie := ""
	access := strings.TrimSpace(f.token)
	refresh := strings.TrimSpace(f.refresh)

	if f.cookie != "" {
		sessionCookie = browser.ParseCookieHeader(f.cookie, "__Secure-next-auth.session-token")
		if sessionCookie == "" {
			sessionCookie = strings.TrimSpace(f.cookie)
		}
	}

	wantBrowser := (f.useBrowser || (access == "" && sessionCookie == "" && !f.noBrowser))
	if wantBrowser {
		fmt.Println("Opening dedicated browser (CDP capture, no Keychain)…")
		tok, err := browser.CaptureCookieViaBrowser(browser.ChatGPTWebLogin, 5*time.Minute)
		if err != nil {
			fmt.Printf("Browser capture failed: %v\n", err)
			if f.noBrowser || f.useBrowser {
				return
			}
			fmt.Println("Falling back to paste…")
		} else {
			sessionCookie = tok
			fmt.Println("Captured session cookie from browser.")
		}
	}

	if sessionCookie == "" && access == "" {
		fmt.Println("Paste from chatgpt.com DevTools, or leave blank to cancel:")
		raw := readLinePrompt("  session-token cookie OR accessToken: ")
		if raw == "" {
			fmt.Println("Cancelled.")
			return
		}
		if strings.HasPrefix(raw, "eyJ") || strings.HasPrefix(raw, "sk-") {
			access = raw
		} else {
			sessionCookie = browser.ParseCookieHeader(raw, "__Secure-next-auth.session-token")
			if sessionCookie == "" {
				sessionCookie = raw
			}
		}
	}

	if access == "" && sessionCookie != "" {
		sess, err := browser.FetchChatGPTSession(sessionCookie)
		if err != nil {
			fmt.Printf("Web session exchange failed: %v\n", err)
			fmt.Println("Falling back to storing the raw cookie/token as-is.")
			access = sessionCookie
		} else {
			access = sess.AccessToken
			if refresh == "" {
				refresh = sess.RefreshToken
			}
			if sess.Email != "" {
				fmt.Printf("Session OK for %s (expires %s).\n", sess.Email, sess.Expires)
			} else {
				fmt.Println("Session OK — access token from chatgpt.com/api/auth/session.")
			}
		}
	}
	if refresh == "" && f.token == "" && f.cookie == "" && !wantBrowser {
		if r := readLinePrompt("  refresh token (optional, Enter to skip): "); r != "" {
			refresh = r
		}
	}

	if access == "" {
		fmt.Println("No token provided. Cancelled.")
		return
	}

	id, priorityFloor, multi := nextPoolID(provider.PoolIDPrefix("chatgpt_web"))
	priority := provider.PriorityWebChatGPT
	if multi {
		priority = priorityFloor
	}
	model := f.model
	if model == "" {
		model = "auto"
	}
	err := provider.AddOrUpdateProvider(provider.DefaultAccountsPath(), provider.ProviderConfig{
		ID:           id,
		Type:         "chatgpt_web",
		Priority:     priority,
		SessionToken: access,
		RefreshToken: refresh,
		Model:        model,
	})
	if err != nil {
		fmt.Printf("Error saving configuration: %v\n", err)
		return
	}
	proxy.Sync()
	fmt.Printf("Saved ChatGPT Web as %s.\n", id)
}

func loginClaude(f loginFlags) {
	fmt.Println("== Login: Claude Web ==")

	key := strings.TrimSpace(f.token)
	if f.cookie != "" {
		key = browser.ParseCookieHeader(f.cookie, "sessionKey")
		if key == "" {
			key = strings.TrimSpace(f.cookie)
		}
	}

	wantBrowser := f.useBrowser || (key == "" && !f.noBrowser)
	cookieHeader := ""
	if wantBrowser && key == "" {
		fmt.Println("Opening dedicated browser (CDP capture, no Keychain)…")
		auth, err := browser.CaptureWebAuthViaBrowser(browser.ClaudeWebLogin, 5*time.Minute)
		if err != nil {
			fmt.Printf("Browser capture failed: %v\n", err)
			if f.useBrowser {
				return
			}
			fmt.Println("Falling back to paste…")
		} else {
			key = auth.SessionValue
			cookieHeader = auth.CookieHeader
			fmt.Println("Captured sessionKey from browser.")
			if cookieHeader != "" {
				n := strings.Count(cookieHeader, "=")
				fmt.Printf("Captured full cookie jar (%d cookies).\n", n)
			} else {
				fmt.Println("Warning: cookie jar empty — Cloudflare cookies missing; re-run login if chat 403s.")
			}
		}
	}

	if key == "" {
		key = readLinePrompt("Paste claude.ai sessionKey cookie (DevTools → Cookies): ")
	}
	if key == "" {
		fmt.Println("Cancelled.")
		return
	}
	if parsed := browser.ParseCookieHeader(key, "sessionKey"); parsed != "" {
		key = parsed
	}
	if cookieHeader == "" && f.cookie != "" && strings.Contains(f.cookie, "=") {
		cookieHeader = f.cookie
	}

	id, priorityFloor, multi := nextPoolID(provider.PoolIDPrefix("claude_web"))
	priority := provider.PriorityWebClaude
	if multi {
		priority = priorityFloor
	}
	model := f.model
	if model == "" {
		model = "claude-sonnet-5"
	}
	err := provider.AddOrUpdateProvider(provider.DefaultAccountsPath(), provider.ProviderConfig{
		ID:         id,
		Type:       "claude_web",
		Priority:   priority,
		SessionKey: key,
		Cookies:    cookieHeader,
		Model:      model,
	})
	if err != nil {
		fmt.Printf("Error saving: %v\n", err)
		return
	}
	proxy.Sync()
	fmt.Printf("Saved Claude Web as %s.\n", id)
}

func loginGemini(f loginFlags) {
	fmt.Println("== Login: Gemini (AI Studio API key) ==")

	key := strings.TrimSpace(f.token)
	if key == "" && !f.noBrowser {
		// Gemini needs an API key; open AI Studio so user can copy one.
		// (Cookie alone cannot call generativelanguage.googleapis.com.)
		fmt.Println("Opening AI Studio — create/copy an API key, then paste it below.")
		_ = exec.Command("open", "https://aistudio.google.com/apikey").Start()
	}
	if key == "" {
		key = readLinePrompt("Paste Google AI Studio API key (Enter = $GOOGLE_AI_STUDIO_KEY): ")
	}
	if key == "" {
		key = "env:GOOGLE_AI_STUDIO_KEY"
	}

	id, priorityFloor, multi := nextPoolID(provider.PoolIDPrefix("gemini"))
	priority := provider.PriorityAPIGemini
	if multi {
		priority = priorityFloor
	}
	model := f.model
	if model == "" {
		model = "gemini-3.6-flash"
	}
	err := provider.AddOrUpdateProvider(provider.DefaultAccountsPath(), provider.ProviderConfig{
		ID:       id,
		Type:     "gemini",
		Priority: priority,
		APIKey:   key,
		Model:    model,
		Cookies:  f.cookie,
	})
	if err != nil {
		fmt.Printf("Error saving: %v\n", err)
		return
	}
	proxy.Sync()
	fmt.Printf("Saved Gemini as %s.\n", id)
}

func loginGeminiWeb(f loginFlags) {
	fmt.Println("== Login: Gemini Web (gemini.google.com) ==")

	cookieHeader := strings.TrimSpace(f.cookie)
	key := strings.TrimSpace(f.token)
	wantBrowser := f.useBrowser || (cookieHeader == "" && key == "" && !f.noBrowser)
	if wantBrowser {
		fmt.Println("Opening dedicated browser (CDP capture) — sign in to gemini.google.com…")
		auth, err := browser.CaptureWebAuthViaBrowser(browser.GeminiWebLogin, 5*time.Minute)
		if err != nil {
			fmt.Printf("Browser capture failed: %v\n", err)
			if f.useBrowser {
				return
			}
			fmt.Println("Falling back to paste…")
		} else {
			key = auth.SessionValue
			cookieHeader = auth.CookieHeader
			fmt.Println("Captured __Secure-1PSID from browser.")
			if cookieHeader != "" {
				fmt.Printf("Captured cookie jar (%d cookies).\n", strings.Count(cookieHeader, "="))
			}
		}
	}
	if cookieHeader == "" && key == "" {
		cookieHeader = readLinePrompt("Paste gemini.google.com Cookie header (needs __Secure-1PSID): ")
	}
	if key == "" && cookieHeader != "" {
		key = browser.ParseCookieHeader(cookieHeader, "__Secure-1PSID")
	}
	if cookieHeader == "" && key != "" {
		cookieHeader = "__Secure-1PSID=" + key
	}
	if cookieHeader == "" {
		fmt.Println("Cancelled.")
		return
	}

	id, priorityFloor, multi := nextPoolID(provider.PoolIDPrefix("gemini_web"))
	priority := provider.PriorityWebGemini
	if multi {
		priority = priorityFloor
	}
	err := provider.AddOrUpdateProvider(provider.DefaultAccountsPath(), provider.ProviderConfig{
		ID:         id,
		Type:       "gemini_web",
		Priority:   priority,
		SessionKey: key,
		Cookies:    cookieHeader,
		Model:      f.model,
	})
	if err != nil {
		fmt.Printf("Error saving: %v\n", err)
		return
	}
	proxy.Sync()
	fmt.Printf("Saved Gemini Web as %s.\n", id)
}

func loginGitHubModels(f loginFlags) {
	fmt.Println("== Login: GitHub Models ==")
	tok := strings.TrimSpace(f.token)
	if tok == "" {
		tok = readLinePrompt("GitHub PAT (Enter = $GITHUB_MODELS_TOKEN): ")
	}
	if tok == "" {
		tok = "env:GITHUB_MODELS_TOKEN"
	}
	id, priorityFloor, multi := nextPoolID("githubapi")
	priority := provider.PriorityAPIGitHub
	if multi {
		priority = priorityFloor
	}
	err := provider.AddOrUpdateProvider(provider.DefaultAccountsPath(), provider.ProviderConfig{
		ID:       id,
		Type:     "openai_compatible",
		Priority: priority,
		BaseURL:  "https://models.github.ai/inference",
		APIKey:   tok,
		Model:    "gpt-4o",
	})
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	proxy.Sync()
	fmt.Printf("Saved GitHub Models as %s.\n", id)
}

func loginGroq(f loginFlags) {
	fmt.Println("== Login: Groq ==")
	key := strings.TrimSpace(f.token)
	if key == "" {
		key = readLinePrompt("Groq API key (Enter = $GROQ_API_KEY): ")
	}
	if key == "" {
		key = "env:GROQ_API_KEY"
	}
	id, priorityFloor, multi := nextPoolID("groqapi")
	priority := provider.PriorityAPIGroq
	if multi {
		priority = priorityFloor
	}
	err := provider.AddOrUpdateProvider(provider.DefaultAccountsPath(), provider.ProviderConfig{
		ID:       id,
		Type:     "openai_compatible",
		Priority: priority,
		BaseURL:  "https://api.groq.com/openai/v1",
		APIKey:   key,
		Model:    "llama-3.3-70b-versatile",
	})
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	proxy.Sync()
	fmt.Printf("Saved Groq as %s.\n", id)
}

// CmdDoctorProviders live-probes every pool adapter with a tiny chat turn and
// prints OK/FAIL. Used to answer "does chatgpt/gemini/ddg/api actually work?".
func CmdDoctorProviders() {
	adapters, err := provider.LoadAccounts(provider.DefaultAccountsPath())
	if err != nil {
		fmt.Printf("load accounts: %v\n", err)
		return
	}
	if len(adapters) == 0 {
		fmt.Println("No providers in pool. Try: am login chatgpt|claude|gemini")
		return
	}
	fmt.Println("=== am doctor providers (live 1-turn probe) ===")
	req := &types.ChatRequest{
		Model:    "default",
		Messages: []types.ChatMessage{{Role: "user", Content: "Reply with exactly: OK"}},
	}
	for _, a := range adapters {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		ch, err := a.SendMessageStream(ctx, req)
		if err != nil {
			cancel()
			fmt.Printf("FAIL  %-16s  %v\n", a.ID(), err)
			continue
		}
		var got strings.Builder
		var streamErr error
		for chunk := range ch {
			if chunk.Error != nil {
				streamErr = chunk.Error
				break
			}
			got.WriteString(chunk.Content)
		}
		cancel()
		if streamErr != nil {
			fmt.Printf("FAIL  %-16s  %v\n", a.ID(), streamErr)
			continue
		}
		preview := strings.ReplaceAll(strings.TrimSpace(got.String()), "\n", " ")
		if len(preview) > 60 {
			preview = preview[:60] + "…"
		}
		if preview == "" {
			fmt.Printf("FAIL  %-16s  empty reply\n", a.ID())
			continue
		}
		fmt.Printf("OK    %-16s  %q\n", a.ID(), preview)
	}
}

// CmdAccounts lists all multi-provider pool accounts, sorted by priority
// (the order the router actually tries them in).
func CmdAccounts() {
	file, err := provider.LoadConfigFile(provider.DefaultAccountsPath())
	var rows []provider.ProviderConfig
	if err == nil && file != nil {
		rows = append(rows, file.Providers...)
	}

	hasCodexRow := false
	for _, p := range rows {
		if p.Type == "codex_cli" {
			hasCodexRow = true
			break
		}
	}
	if !hasCodexRow {
		if row, ok := provider.CodexAutoRow(rows); ok {
			rows = append(rows, row)
		}
	}

	if len(rows) == 0 {
		fmt.Println("No accounts configured in pool yet. Run 'amux login <provider>' or create ~/.am/accounts.json.")
		return
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Priority < rows[j].Priority })

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "ID\tTYPE\tPRIORITY\tTARGET MODEL\tAUTH CONFIGURED")
	fmt.Fprintln(w, "--\t----\t--------\t------------\t---------------")

	for _, p := range rows {
		authSet := "No"
		switch p.Type {
		case "openai_compatible", "gemini":
			if provider.ResolveSecret(p.APIKey) != "" {
				authSet = "Yes (API Key)"
			}
		case "chatgpt_web":
			if provider.ResolveSecret(p.SessionToken) != "" {
				authSet = "Yes (Session Token)"
			}
		case "claude_web":
			if provider.ResolveSecret(p.SessionKey) != "" {
				authSet = "Yes (Session Key)"
			}
		case "codex_cli":
			authSet = "Yes (reused from `am add codex`)"
		}

		model := p.Model
		if model == "" {
			model = "-"
		}

		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", p.ID, p.Type, p.Priority, model, authSet)
	}
	w.Flush()
}

// CmdAccountsCmd handles `am accounts [priority <id> <N>]`.
func CmdAccountsCmd(args []string) {
	if len(args) == 0 {
		CmdAccounts()
		return
	}

	switch args[0] {
	case "ls", "list":
		CmdAccounts()
	case "rm", "delete", "remove":
		if len(args) < 2 {
			fmt.Println("Usage: amux accounts rm <id>")
			fmt.Println("   or: amux api rm <id>")
			return
		}
		if err := provider.RemoveProvider(provider.DefaultAccountsPath(), args[1]); err != nil {
			fmt.Printf("Error removing provider: %v\n", err)
			return
		}
		proxy.Sync()
		fmt.Printf("Removed provider %q from pool\n", args[1])
	case "priority":
		if len(args) < 3 {
			fmt.Println("Usage: amux accounts priority <id> <N>")
			return
		}
		n, err := strconv.Atoi(args[2])
		if err != nil {
			fmt.Printf("invalid priority %q: %v\n", args[2], err)
			return
		}
		if err := provider.SetPriority(provider.DefaultAccountsPath(), args[1], n); err != nil {
			fmt.Printf("Error setting priority: %v\n", err)
			return
		}
		proxy.Sync()
		fmt.Printf("set %s priority to %d\n", args[1], n)
	case "model":
		if len(args) < 3 {
			fmt.Println("Usage: amux accounts model <id> <model>")
			return
		}
		if err := provider.SetModel(provider.DefaultAccountsPath(), args[1], args[2]); err != nil {
			fmt.Printf("Error setting model: %v\n", err)
			return
		}
		proxy.Sync()
		fmt.Printf("set %s model to %s\n", args[1], args[2])
	default:
		fmt.Println("Usage: amux accounts [ls | rm <id> | priority <id> <N> | model <id> <model>]")
	}
}

// CmdAPI handles 'am api add', 'am api rm', 'am api ls'.
func CmdAPI(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: amux api [add | rm | ls]")
		return
	}

	switch args[0] {
	case "ls", "list":
		CmdAccounts()
	case "rm", "delete":
		if len(args) < 2 {
			fmt.Println("Usage: amux api rm <name>")
			return
		}
		if err := provider.RemoveProvider(provider.DefaultAccountsPath(), args[1]); err != nil {
			fmt.Printf("Error removing provider: %v\n", err)
			return
		}
		proxy.Sync()
		fmt.Printf("Removed provider %q from pool\n", args[1])
	case "add":
		name := ""
		endpoint := ""
		apiKey := ""
		model := "default"
		priority := provider.PriorityAPICustom

		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--endpoint":
				if i+1 < len(args) {
					endpoint = args[i+1]
					i++
				}
			case "--api-key":
				if i+1 < len(args) {
					apiKey = args[i+1]
					i++
				}
			case "--model":
				if i+1 < len(args) {
					model = args[i+1]
					i++
				}
			case "--priority":
				if i+1 < len(args) {
					p, _ := strconv.Atoi(args[i+1])
					if p > 0 {
						priority = p
					}
					i++
				}
			default:
				if name == "" && !strings.HasPrefix(args[i], "-") {
					name = args[i]
				}
			}
		}

		if name == "" || endpoint == "" {
			fmt.Println("Usage: amux api add <name> --endpoint <url> --api-key <key> [--model M] [--priority N]")
			return
		}

		err := provider.AddOrUpdateProvider(provider.DefaultAccountsPath(), provider.ProviderConfig{
			ID:       name,
			Type:     "openai_compatible",
			Priority: priority,
			BaseURL:  endpoint,
			APIKey:   apiKey,
			Model:    model,
		})
		if err != nil {
			fmt.Printf("Error adding provider: %v\n", err)
			return
		}
		proxy.Sync()
		fmt.Printf("Added OpenAI-compatible provider %q to pool (priority %d)\n", name, priority)
	}
}
