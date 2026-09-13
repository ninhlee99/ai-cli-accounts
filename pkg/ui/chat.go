package ui

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"amux-accounts/pkg/monitor"
	"amux-accounts/pkg/provider"
	"amux-accounts/pkg/router"
	"amux-accounts/pkg/term"
	"amux-accounts/pkg/types"
)

// CmdChat runs a Claude-Code-like terminal chat session backed by the pool.
// Flags: --provider/-p <id> pins one adapter.
func CmdChat(args []string) {
	providerID, args := extractProviderFlag(args)

	adapters, _ := provider.LoadAccounts(provider.DefaultAccountsPath())
	if len(adapters) == 0 {
		term.Warn("No providers in pool. Run `am login chatgpt` or `am login gemini`.")
		return
	}
	if providerID != "" {
		var match types.ProviderAdapter
		var ids []string
		for _, a := range adapters {
			ids = append(ids, a.ID())
			if a.ID() == providerID {
				match = a
			}
		}
		if match == nil {
			term.Error("No provider %q. Available: %s", providerID, strings.Join(ids, ", "))
			return
		}
		adapters = []types.ProviderAdapter{match}
	}

	pool := router.NewAccountPoolRouter(adapters)
	var history []types.ChatMessage

	ids := make([]string, 0, len(adapters))
	for _, a := range adapters {
		ids = append(ids, a.ID())
	}

	term.SetQuiet(true)
	defer term.SetQuiet(false)

	printChatSplash(ids)

	if len(args) > 0 {
		runClaudeTurn(pool, &history, strings.Join(args, " "))
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		printUserPrompt()
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" || line == "/exit" {
			break
		}
		if line == "/clear" {
			history = nil
			fmt.Print("\x1b[H\x1b[2J")
			printChatSplash(ids)
			continue
		}
		if line == "/help" {
			printChatHelp()
			continue
		}
		runClaudeTurn(pool, &history, line)
	}
	fmt.Println()
	fmt.Println(term.Dim("  bye"))
}

func printChatSplash(providerIDs []string) {
	fmt.Println()
	term.LogoSmall()
	fmt.Println()
	n := len(providerIDs)
	fmt.Printf("  %s  %s\n",
		term.Dim("chat"),
		term.Dim(fmt.Sprintf("%d provider%s · /help · exit", n, plural(n))),
	)
	fmt.Println(term.Cyan("  " + strings.Repeat("-", 42)))
	fmt.Println()
}

func printChatHelp() {
	fmt.Println()
	fmt.Println(term.Dim("  /clear   reset conversation"))
	fmt.Println(term.Dim("  /help    this help"))
	fmt.Println(term.Dim("  exit     quit"))
	fmt.Println(term.Dim("  -p id    pin provider (amux chat -p claudeweb:01)"))
	fmt.Println()
}

func printUserPrompt() {
	// Fixed-width role label so cursor lines up every turn.
	fmt.Print(term.Cyan(term.Bold(padRole("you"))) + term.Dim(" › "))
}

func printAssistantHeader(who string) {
	fmt.Println()
	fmt.Printf("%s %s\n",
		term.Green(term.Bold(padRole("amux"))),
		term.Dim("· "+who),
	)
	fmt.Print(term.Green("│ "))
}

func printAssistantFooter(d time.Duration) {
	fmt.Println()
	fmt.Println(term.Dim(fmt.Sprintf("%s %s", padRole(""), d.Round(10*time.Millisecond))))
	fmt.Println()
}

func padRole(s string) string {
	// "amux" / "you " — 4 runes wide for column alignment.
	const w = 4
	n := utf8.RuneCountInString(s)
	if n >= w {
		return s
	}
	return s + strings.Repeat(" ", w-n)
}

func runClaudeTurn(pool *router.AccountPoolRouter, history *[]types.ChatMessage, prompt string) {
	*history = append(*history, types.ChatMessage{Role: "user", Content: prompt})

	req := &types.ChatRequest{
		Model:         "default",
		Messages:      *history,
		Stream:        true,
		FullContext:   true,
		ClientDialect: "chat",
	}

	started := time.Now()
	ctx := context.Background()
	ch, err := pool.Send(ctx, req)
	if err != nil {
		*history = (*history)[:len(*history)-1]
		fmt.Println()
		fmt.Println(term.Red(padRole("err!")) + " " + err.Error())
		fmt.Println()
		monitor.AppendRequest(types.RequestEntry{
			Time: time.Now(), Dialect: "chat", Account: pool.LastUsed(),
			Input: prompt, Error: err.Error(), DurationMs: time.Since(started).Milliseconds(),
		})
		monitor.AppendFullIO(monitor.FullIO{
			Time: time.Now(), Account: pool.LastUsed(), Dialect: "chat",
			Error: err.Error(), DurationMs: time.Since(started).Milliseconds(),
			Messages: []types.ChatMessage{{Role: "user", Content: prompt}},
		})
		return
	}

	who := pool.LastUsed()
	if who == "" {
		who = "assistant"
	}
	printAssistantHeader(who)

	var full strings.Builder
	var tools []string
	streamErr := ""
	for chunk := range ch {
		if chunk.Error != nil {
			streamErr = chunk.Error.Error()
			break
		}
		if len(chunk.ToolCalls) > 0 {
			for _, tc := range chunk.ToolCalls {
				tools = append(tools, tc.Name)
			}
		}
		if chunk.Content == "" {
			continue
		}
		text := chunk.Content
		// Indent wrapped lines under the assistant gutter.
		if strings.Contains(text, "\n") {
			parts := strings.Split(text, "\n")
			for i, p := range parts {
				if i > 0 {
					fmt.Print("\n" + term.Green("│ "))
				}
				fmt.Print(p)
			}
		} else {
			fmt.Print(text)
		}
		full.WriteString(chunk.Content)
	}
	fmt.Println()
	if len(tools) > 0 {
		fmt.Println(term.Dim(padRole("") + " tools · " + strings.Join(tools, ", ")))
	}
	printAssistantFooter(time.Since(started))

	if streamErr != "" {
		fmt.Println(term.Red(padRole("err!")) + " " + streamErr)
		fmt.Println()
	}

	out := full.String()
	if out != "" {
		*history = append(*history, types.ChatMessage{Role: "assistant", Content: out})
	} else if streamErr == "" {
		*history = (*history)[:len(*history)-1]
	}

	monitor.AppendRequest(types.RequestEntry{
		Time:       time.Now(),
		Dialect:    "chat",
		Account:    who,
		Input:      prompt,
		Output:     out,
		DurationMs: time.Since(started).Milliseconds(),
		Error:      streamErr,
		StopReason: "end_turn",
	})
	monitor.AppendFullIO(monitor.FullIO{
		Time:       time.Now(),
		Account:    who,
		Dialect:    "chat",
		Stop:       "end_turn",
		Error:      streamErr,
		DurationMs: time.Since(started).Milliseconds(),
		Messages:   []types.ChatMessage{{Role: "user", Content: prompt}},
		Output:     out,
	})
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func extractProviderFlag(args []string) (id string, rest []string) {
	for i := 0; i < len(args); i++ {
		if (args[i] == "--provider" || args[i] == "-p") && i+1 < len(args) {
			id = args[i+1]
			rest = append(rest, args[:i]...)
			rest = append(rest, args[i+2:]...)
			return id, rest
		}
	}
	return "", args
}
