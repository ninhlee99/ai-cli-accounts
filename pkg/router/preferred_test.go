package router

import (
	"context"
	"errors"
	"testing"

	"amux-accounts/pkg/types"
)

type dummyAdapter struct {
	id       string
	priority int
	called   bool
}

func (d *dummyAdapter) ID() string    { return d.id }
func (d *dummyAdapter) Priority() int { return d.priority }
func (d *dummyAdapter) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	d.called = true
	ch := make(chan types.StreamChunk, 1)
	ch <- types.StreamChunk{ID: d.id, Content: "ok", Done: true}
	close(ch)
	return ch, nil
}

func TestAccountPoolRouter_Preferred(t *testing.T) {
	a1 := &dummyAdapter{id: "p1", priority: 1}
	a2 := &dummyAdapter{id: "p2", priority: 2}

	pool := NewAccountPoolRouter([]types.ProviderAdapter{a1, a2})
	pool.SetPreferred("p2")

	if pool.Preferred() != "p2" {
		t.Fatalf("expected preferred to be p2, got %s", pool.Preferred())
	}

	ch, err := pool.Send(context.Background(), &types.ChatRequest{Model: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	chunk := <-ch
	if chunk.ID != "p2" {
		t.Fatalf("expected chunk from preferred p2, got %s", chunk.ID)
	}
	if !a2.called {
		t.Errorf("expected a2 to be called first")
	}
	if a1.called {
		t.Errorf("p1 must not be called when p2 is preferred")
	}
	if pool.LastUsed() != "p2" {
		t.Fatalf("last used want p2, got %s", pool.LastUsed())
	}
}

func TestAccountPoolRouter_PreferredHardPinNoFailover(t *testing.T) {
	a1 := &dummyAdapter{id: "geminiapi:01", priority: 1}
	a2 := &failAdapter{id: "chatgptweb:01", priority: 20, err: errors.New("upstream 403")}

	pool := NewAccountPoolRouter([]types.ProviderAdapter{a1, a2})
	pool.SetPreferred("chatgptweb:01")

	_, err := pool.Send(context.Background(), &types.ChatRequest{Model: "test"})
	if err == nil {
		t.Fatal("expected preferred failure to surface, not failover")
	}
	if a1.called {
		t.Fatal("gemini must not be called when preferred chatgptweb fails non-429")
	}
	if !a2.called {
		t.Fatal("expected preferred chatgptweb to be tried")
	}
	if pool.Preferred() != "chatgptweb:01" {
		t.Fatalf("preferred should stay pinned on non-429 fail, got %s", pool.Preferred())
	}
}

func TestAccountPoolRouter_AutoSwitchPromotesPreferred(t *testing.T) {
	a1 := &failAdapter{id: "chatgptweb:01", priority: 1, err: types.ErrRateLimitReached}
	a2 := &dummyAdapter{id: "geminiapi:01", priority: 2}

	pool := NewAccountPoolRouter([]types.ProviderAdapter{a1, a2})
	pool.SetPreferred("chatgptweb:01")

	ch, err := pool.Send(context.Background(), &types.ChatRequest{Model: "test"})
	if err != nil {
		t.Fatalf("expected failover success: %v", err)
	}
	<-ch
	if !a1.called || !a2.called {
		t.Fatal("expected both preferred (429) and failover winner called")
	}
	if pool.Preferred() != "geminiapi:01" {
		t.Fatalf("auto-switch should promote winner to preferred, got %s", pool.Preferred())
	}
	if pool.LastUsed() != "geminiapi:01" {
		t.Fatalf("last used want geminiapi:01, got %s", pool.LastUsed())
	}
}

type failAdapter struct {
	id       string
	priority int
	err      error
	called   bool
}

func (d *failAdapter) ID() string    { return d.id }
func (d *failAdapter) Priority() int { return d.priority }
func (d *failAdapter) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	d.called = true
	return nil, d.err
}
