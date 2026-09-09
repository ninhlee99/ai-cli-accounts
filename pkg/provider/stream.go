package provider

import (
	"context"

	"amux-accounts/pkg/types"
)

func sendChunk(ctx context.Context, ch chan<- types.StreamChunk, chunk types.StreamChunk) bool {
	select {
	case <-ctx.Done():
		return false
	case ch <- chunk:
		return true
	}
}
