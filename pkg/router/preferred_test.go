package router

import (
	"context"
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
}
