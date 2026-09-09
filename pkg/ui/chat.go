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
func CmdChat(args []string) {
	adapters, err := provider.LoadAccounts(provider.DefaultAccountsPath())
	if err != nil || len(adapters) == 0 {
		adapters = []types.ProviderAdapter{
			&provider.DuckDuckGoAdapter{
				AdapterID:   "duckduckgo",
				TargetModel: "claude-3-haiku-20240307",
				PriorityLvl: 99,
			},
		}
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
		fmt.Printf("\n[Error: %v]\n", err)
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
