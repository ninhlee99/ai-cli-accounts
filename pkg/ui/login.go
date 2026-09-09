package ui

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"amux-accounts/pkg/browser"
	"amux-accounts/pkg/provider"
	"amux-accounts/pkg/proxy"
	"amux-accounts/pkg/types"
)

// CmdLogin handles the interactive login flow for supported providers.
func CmdLogin(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: amux login <provider> [--model M]")
		fmt.Println("Providers: chatgpt, claude, gemini, github, groq")
		return
	}

	target := strings.ToLower(args[0])
	model := ""
	for i := 1; i < len(args); i++ {
		if args[i] == "--model" && i+1 < len(args) {
			model = args[i+1]
			i++
		}
	}

	switch target {
	case "chatgpt":
		loginChatGPT(model)
	case "claude":
		loginClaude(model)
	case "gemini":
		loginGemini(model)
	case "github", "github-models":
		loginGitHubModels()
	case "groq":
		loginGroq()
	default:
		fmt.Printf("Unknown provider %q. Supported: chatgpt, claude, gemini, github, groq\n", target)
	}
}

func readLinePrompt(prompt string) string {
	fmt.Print(prompt)
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

// nextPoolID returns the next free unified ID for a given prefix, and
// whether any session of that prefix already exists (the multi-session
// case, where a second/third login should slot in behind the existing
// one(s) rather than compete for the top of the queue).
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

func loginChatGPT(model string) {
	fmt.Println("== Login: ChatGPT Web ==")
	tok, bName, err := browser.ExtractCookie("chatgpt.com", "__Secure-next-auth.session-token")
	if err != nil || tok == "" {
		tok, bName, err = browser.ExtractCookie("openai.com", "__Secure-next-auth.session-token")
	}

	if err == nil && tok != "" {
		fmt.Printf("Found ChatGPT session token from browser (%s) automatically.\n", bName)
		if accToken, err := browser.FetchChatGPTSessionAccessToken(tok); err == nil && accToken != "" {
			tok = accToken
		}
	} else {
		tok = readLinePrompt("Enter ChatGPT session token (__Secure-next-auth.session-token): ")
	}

	if tok == "" {
		fmt.Println("No session token provided. Cancelled.")
		return
	}

	id, priorityFloor, multi := nextPoolID(provider.PoolIDPrefix("chatgpt_web"))
	priority := 5
	if multi {
		priority = priorityFloor
	}

	if model == "" {
		model = "auto"
	}
	err = provider.AddOrUpdateProvider(provider.DefaultAccountsPath(), provider.ProviderConfig{
		ID:           id,
		Type:         "chatgpt_web",
		Priority:     priority,
		SessionToken: tok,
		Model:        model,
	})
	if err != nil {
		fmt.Printf("Error saving configuration: %v\n", err)
		return
	}
	proxy.Sync()
	fmt.Printf("Successfully saved ChatGPT Web account to pool as %s!\n", id)
}

func loginClaude(model string) {
	fmt.Println("== Login: Claude Web ==")
	key, bName, err := browser.ExtractCookie("claude.ai", "sessionKey")
	if err == nil && key != "" {
		fmt.Printf("Found Claude sessionKey from browser (%s) automatically.\n", bName)
	} else {
		key = readLinePrompt("Enter Claude Web sessionKey cookie: ")
	}

	if key == "" {
		fmt.Println("No sessionKey provided. Cancelled.")
		return
	}

	id, priorityFloor, multi := nextPoolID(provider.PoolIDPrefix("claude_web"))
	priority := 6
	if multi {
		priority = priorityFloor
	}

	if model == "" {
		model = "claude-3-5-sonnet-20241022"
	}
	err = provider.AddOrUpdateProvider(provider.DefaultAccountsPath(), provider.ProviderConfig{
		ID:         id,
		Type:       "claude_web",
		Priority:   priority,
		SessionKey: key,
		Model:      model,
	})
	if err != nil {
		fmt.Printf("Error saving configuration: %v\n", err)
		return
	}
	proxy.Sync()
	fmt.Printf("Successfully saved Claude Web account to pool as %s!\n", id)
}

func loginGemini(model string) {
	fmt.Println("== Login: Google AI Studio (Gemini) ==")
	key := readLinePrompt("Enter Google AI Studio API Key (or press Enter to read from $GOOGLE_AI_STUDIO_KEY): ")
	if key == "" {
		key = "env:GOOGLE_AI_STUDIO_KEY"
	}

	id, priorityFloor, multi := nextPoolID(provider.PoolIDPrefix("gemini"))
	priority := 2
	if multi {
		priority = priorityFloor
	}

	if model == "" {
		model = "gemini-2.0-flash"
	}
	err := provider.AddOrUpdateProvider(provider.DefaultAccountsPath(), provider.ProviderConfig{
		ID:       id,
		Type:     "gemini",
		Priority: priority,
		APIKey:   key,
		Model:    model,
	})
	if err != nil {
		fmt.Printf("Error saving configuration: %v\n", err)
		return
	}
	proxy.Sync()
	fmt.Printf("Successfully saved Google AI Studio account to pool as %s!\n", id)
}

func loginGitHubModels() {
	fmt.Println("== Login: GitHub Models ==")
	tok := readLinePrompt("Enter GitHub Personal Access Token (or press Enter to read from $GITHUB_MODELS_TOKEN): ")
	if tok == "" {
		tok = "env:GITHUB_MODELS_TOKEN"
	}

	id, priorityFloor, multi := nextPoolID("githubapi")
	priority := 1
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
		fmt.Printf("Error saving configuration: %v\n", err)
		return
	}
	proxy.Sync()
	fmt.Printf("Successfully saved GitHub Models account to pool as %s!\n", id)
}

func loginGroq() {
	fmt.Println("== Login: Groq ==")
	key := readLinePrompt("Enter Groq API Key (or press Enter to read from $GROQ_API_KEY): ")
	if key == "" {
		key = "env:GROQ_API_KEY"
	}

	id, priorityFloor, multi := nextPoolID("groqapi")
	priority := 3
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
		fmt.Printf("Error saving configuration: %v\n", err)
		return
	}
	proxy.Sync()
	fmt.Printf("Successfully saved Groq account to pool as %s!\n", id)
}

// CmdAccounts lists all multi-provider pool accounts, sorted by priority
// (the order the router actually tries them in).
func CmdAccounts() {
	file, err := provider.LoadConfigFile(provider.DefaultAccountsPath())
	var rows []provider.ProviderConfig
	if err == nil && file != nil {
		rows = append(rows, file.Providers...)
	}

	// Auto-surface the Codex CLI token-reuse adapter (see
	// provider.CodexAutoRow) even when it has no real accounts.json entry
	// yet — it appears the moment `am add codex` has a live login, no
	// separate `am login codex` step needed.
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

	// Same idea for the auto-surfaced DuckDuckGo fallback (see
	// provider.DuckDuckGoAutoRow) — it has no real accounts.json entry
	// either, unless the user has explicitly configured or disabled it.
	hasDuckDuckGoRow := false
	for _, p := range rows {
		if p.Type == "duckduckgo" {
			hasDuckDuckGoRow = true
			break
		}
	}
	if !hasDuckDuckGoRow {
		if row, ok := provider.DuckDuckGoAutoRow(rows); ok {
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
		case "duckduckgo":
			if p.Enabled != nil && *p.Enabled {
				authSet = "Yes (Enabled)"
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

// CmdAccountsCmd handles `am accounts [priority <id> <N>]`. With no args it
// prints today's table (CmdAccounts). `priority <id> <N>` sets one
// provider's priority and hot-reloads the running proxy, if any.
func CmdAccountsCmd(args []string) {
	if len(args) == 0 {
		CmdAccounts()
		return
	}

	switch args[0] {
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
		fmt.Println("Usage: amux accounts [priority <id> <N> | model <id> <model>]")
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
		priority := 10

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
