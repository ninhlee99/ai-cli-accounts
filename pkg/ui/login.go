package ui

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"ai-cli-accounts/pkg/browser"
	"ai-cli-accounts/pkg/provider"
)

// CmdLogin handles the interactive login flow for supported providers.
func CmdLogin(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: am login <provider>")
		fmt.Println("Providers: chatgpt, claude, gemini, github, groq")
		return
	}

	target := strings.ToLower(args[0])
	switch target {
	case "chatgpt":
		loginChatGPT()
	case "claude":
		loginClaude()
	case "gemini":
		loginGemini()
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

func loginChatGPT() {
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

	err = provider.AddOrUpdateProvider(provider.DefaultAccountsPath(), provider.ProviderConfig{
		ID:           "chatgpt-web",
		Type:         "chatgpt_web",
		Priority:     5,
		SessionToken: tok,
		Model:        "auto",
	})
	if err != nil {
		fmt.Printf("Error saving configuration: %v\n", err)
		return
	}
	fmt.Println("Successfully saved ChatGPT Web account to pool!")
}

func loginClaude() {
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

	err = provider.AddOrUpdateProvider(provider.DefaultAccountsPath(), provider.ProviderConfig{
		ID:          "claude-web",
		Type:        "claude_web",
		Priority:    6,
		SessionKey:  key,
		Model:       "claude-3-5-sonnet-20241022",
	})
	if err != nil {
		fmt.Printf("Error saving configuration: %v\n", err)
		return
	}
	fmt.Println("Successfully saved Claude Web account to pool!")
}

func loginGemini() {
	fmt.Println("== Login: Google AI Studio (Gemini) ==")
	key := readLinePrompt("Enter Google AI Studio API Key (or press Enter to read from $GOOGLE_AI_STUDIO_KEY): ")
	if key == "" {
		key = "env:GOOGLE_AI_STUDIO_KEY"
	}

	err := provider.AddOrUpdateProvider(provider.DefaultAccountsPath(), provider.ProviderConfig{
		ID:       "google-ai-studio",
		Type:     "gemini",
		Priority: 2,
		APIKey:   key,
		Model:    "gemini-2.0-flash",
	})
	if err != nil {
		fmt.Printf("Error saving configuration: %v\n", err)
		return
	}
	fmt.Println("Successfully saved Google AI Studio account to pool!")
}

func loginGitHubModels() {
	fmt.Println("== Login: GitHub Models ==")
	tok := readLinePrompt("Enter GitHub Personal Access Token (or press Enter to read from $GITHUB_MODELS_TOKEN): ")
	if tok == "" {
		tok = "env:GITHUB_MODELS_TOKEN"
	}

	err := provider.AddOrUpdateProvider(provider.DefaultAccountsPath(), provider.ProviderConfig{
		ID:       "github-models",
		Type:     "openai_compatible",
		Priority: 1,
		BaseURL:  "https://models.github.ai/inference",
		APIKey:   tok,
		Model:    "gpt-4o",
	})
	if err != nil {
		fmt.Printf("Error saving configuration: %v\n", err)
		return
	}
	fmt.Println("Successfully saved GitHub Models account to pool!")
}

func loginGroq() {
	fmt.Println("== Login: Groq ==")
	key := readLinePrompt("Enter Groq API Key (or press Enter to read from $GROQ_API_KEY): ")
	if key == "" {
		key = "env:GROQ_API_KEY"
	}

	err := provider.AddOrUpdateProvider(provider.DefaultAccountsPath(), provider.ProviderConfig{
		ID:       "groq",
		Type:     "openai_compatible",
		Priority: 3,
		BaseURL:  "https://api.groq.com/openai/v1",
		APIKey:   key,
		Model:    "llama-3.3-70b-versatile",
	})
	if err != nil {
		fmt.Printf("Error saving configuration: %v\n", err)
		return
	}
	fmt.Println("Successfully saved Groq account to pool!")
}

// CmdAccounts lists all multi-provider pool accounts.
func CmdAccounts() {
	file, err := provider.LoadConfigFile(provider.DefaultAccountsPath())
	if err != nil || file == nil || len(file.Providers) == 0 {
		fmt.Println("No accounts configured in pool yet. Run 'am login <provider>' or create ~/.am/accounts.json.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "ID\tTYPE\tPRIORITY\tTARGET MODEL\tAUTH CONFIGURED")
	fmt.Fprintln(w, "--\t----\t--------\t------------\t---------------")

	for _, p := range file.Providers {
		authSet := "No"
		switch p.Type {
		case "openai_compatible", "gemini":
			if p.APIKey != "" {
				authSet = "Yes (API Key)"
			}
		case "chatgpt_web":
			if p.SessionToken != "" {
				authSet = "Yes (Session Token)"
			}
		case "claude_web":
			if p.SessionKey != "" {
				authSet = "Yes (Session Key)"
			}
		case "duckduckgo":
			authSet = "Yes (Anonymous/Free)"
		}

		model := p.Model
		if model == "" {
			model = "-"
		}

		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", p.ID, p.Type, p.Priority, model, authSet)
	}
	w.Flush()
}

// CmdAPI handles 'am api add', 'am api rm', 'am api ls'.
func CmdAPI(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: am api [add | rm | ls]")
		return
	}

	switch args[0] {
	case "ls", "list":
		CmdAccounts()
	case "rm", "delete":
		if len(args) < 2 {
			fmt.Println("Usage: am api rm <name>")
			return
		}
		if err := provider.RemoveProvider(provider.DefaultAccountsPath(), args[1]); err != nil {
			fmt.Printf("Error removing provider: %v\n", err)
			return
		}
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
			fmt.Println("Usage: am api add <name> --endpoint <url> --api-key <key> [--model M] [--priority N]")
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
		fmt.Printf("Added OpenAI-compatible provider %q to pool (priority %d)\n", name, priority)
	}
}
