package router_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"amux-accounts/pkg/router"
	"amux-accounts/pkg/types"
)

type mockAdapter struct {
	id       string
	priority int
	err      error
	content  string
}

func (m *mockAdapter) ID() string    { return m.id }
func (m *mockAdapter) Priority() int { return m.priority }
func (m *mockAdapter) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	if m.err != nil {
		return nil, m.err
	}
	ch := make(chan types.StreamChunk, 2)
	ch <- types.StreamChunk{ID: m.id, Content: m.content}
	ch <- types.StreamChunk{ID: m.id, Done: true}
	close(ch)
	return ch, nil
}

func TestAccountPoolRouter_Failover(t *testing.T) {
	// Adapter 1: priority 1, fails with 429
	a1 := &mockAdapter{id: "provider-1", priority: 1, err: types.ErrRateLimitReached}
	// Adapter 2: priority 2, succeeds
	a2 := &mockAdapter{id: "provider-2", priority: 2, content: "hello from provider 2"}

	r := router.NewAccountPoolRouter([]types.ProviderAdapter{a2, a1}) // unordered
	ch, err := r.Send(context.Background(), &types.ChatRequest{
		Messages: []types.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	var text string
	for chunk := range ch {
		text += chunk.Content
	}

	if text != "hello from provider 2" {
		t.Fatalf("expected 'hello from provider 2', got %q", text)
	}

	// Now provider-1 should be in cooldown, so next Send goes directly to provider-2
	status := r.Status()
	if len(status) != 2 {
		t.Fatalf("expected 2 providers in status, got %d", len(status))
	}
	if !status[0]["cooling"].(bool) {
		t.Fatalf("expected provider-1 to be cooling")
	}
}

// TestAccountPoolRouter_MultiSessionFailover locks in the multi-session
// scenario Phase 3/4 unlock: two accounts of the *same* provider type
// (unified IDs sharing a prefix, e.g. from a second `am login claude`)
// failing over to each other exactly like two different provider types do —
// the router itself needs no ID-format awareness at all.
func TestAccountPoolRouter_MultiSessionFailover(t *testing.T) {
	session1 := &mockAdapter{id: "claudeweb:01", priority: 1, err: types.ErrRateLimitReached}
	session2 := &mockAdapter{id: "claudeweb:02", priority: 2, content: "hello from session 2"}

	r := router.NewAccountPoolRouter([]types.ProviderAdapter{session1, session2})
	ch, err := r.Send(context.Background(), &types.ChatRequest{
		Messages: []types.ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	var text string
	for chunk := range ch {
		text += chunk.Content
	}
	if text != "hello from session 2" {
		t.Fatalf("expected failover to claudeweb:02, got %q", text)
	}

	status := r.Status()
	if len(status) != 2 {
		t.Fatalf("expected 2 providers in status, got %d", len(status))
	}
	if !status[0]["cooling"].(bool) {
		t.Fatalf("expected claudeweb:01 to be cooling after rate limit")
	}
}

func TestAccountPoolRouter_AllFail(t *testing.T) {
	a1 := &mockAdapter{id: "p1", priority: 1, err: errors.New("err1")}
	a2 := &mockAdapter{id: "p2", priority: 2, err: errors.New("err2")}

	r := router.NewAccountPoolRouter([]types.ProviderAdapter{a1, a2})
	_, err := r.Send(context.Background(), &types.ChatRequest{})
	if err == nil {
		t.Fatalf("expected error when all fail")
	}
}

func TestAccountPoolRouter_ConcurrentRace(t *testing.T) {
	a1 := &mockAdapter{id: "p1", priority: 1, err: types.ErrRateLimitReached}
	a2 := &mockAdapter{id: "p2", priority: 2, content: "p2"}
	a3 := &mockAdapter{id: "p3", priority: 3, content: "p3"}

	r := router.NewAccountPoolRouter([]types.ProviderAdapter{a1, a2, a3})

	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx := context.Background()

			// Concurrently read status
			_ = r.Status()

			// Concurrently read preferred
			_ = r.Preferred()

			// Occasionally update preferred
			if idx%5 == 0 {
				r.SetPreferred("p3")
			}

			// Concurrently send requests
			ch, err := r.Send(ctx, &types.ChatRequest{Model: "test"})
			if err == nil {
				for range ch {
				}
			}
		}(i)
	}
	wg.Wait()
}
