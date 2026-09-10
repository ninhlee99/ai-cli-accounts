package ui

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"amux-accounts/pkg/provider"
	"amux-accounts/pkg/router"
	"amux-accounts/pkg/types"
)

// CmdChat runs a standalone terminal chat session backed by the pool router.
// A leading "--provider <id>" (or "-p <id>") pins the session to that one
// pool adapter instead of the whole pool with failover.
func CmdChat(args []string) {
	providerID, args := extractProviderFlag(args)

	adapters, _ := provider.LoadAccounts(provider.DefaultAccountsPath())
	if len(adapters) == 0 {
		fmt.Println("No providers in pool. Run `am login chatgpt` or `am login gemini` (or add an API key).")
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
			fmt.Printf("No provider %q in pool. Available: %s\n", providerID, strings.Join(ids, ", "))
			return
		}
		adapters = []types.ProviderAdapter{match}
	}

	pool := router.NewAccountPoolRouter(adapters)
	var history []types.ChatMessage

	fmt.Println("=== am interactive chat ===")
	fmt.Println("Type your message. Type 'exit' or Ctrl+D to quit.")
	fmt.Println()

	if len(args) > 0 {
		prompt := strings.Join(args, " ")
		runChatTurn(pool, &history, prompt)
	}

	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("\nuser > ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}

		runChatTurn(pool, &history, line)
	}
	fmt.Println("\nBye!")
}

// extractProviderFlag pulls a leading "--provider <id>" or "-p <id>" out of
// args, returning the id (empty if absent) and the remaining args.
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

func runChatTurn(pool *router.AccountPoolRouter, history *[]types.ChatMessage, prompt string) {
	*history = append(*history, types.ChatMessage{Role: "user", Content: prompt})

	req := &types.ChatRequest{
		Model:    "default",
		Messages: *history,
		Stream:   true,
	}

	ctx := context.Background()
	ch, err := pool.Send(ctx, req)
	if err != nil {
		if strings.Contains(err.Error(), "rate limit") {
			fmt.Printf("\n[Rate limit — Claude free/web giới hạn tin nhắn. Đợi 1–2 phút hoặc dùng geminiapi / Pro.]\n")
			fmt.Printf("[%v]\n", err)
			// Drop the user turn so retrying the same question doesn't stack history.
			*history = (*history)[:len(*history)-1]
			return
		}
		fmt.Printf("\n[Error: %v]\n", err)
		*history = (*history)[:len(*history)-1]
		return
	}

	fmt.Print("\nassistant > ")
	var full strings.Builder
	for chunk := range ch {
		if chunk.Error != nil {
			fmt.Printf("\n[Stream Error: %v]\n", chunk.Error)
			break
		}
		if chunk.Content != "" {
			fmt.Print(chunk.Content)
			full.WriteString(chunk.Content)
		}
	}
	fmt.Println()

	if full.Len() > 0 {
		*history = append(*history, types.ChatMessage{Role: "assistant", Content: full.String()})
	}
}
