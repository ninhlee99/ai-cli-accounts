// Package router picks which provider adapter handles a chat request,
// failing over to the next one when the current one is rate-limited.
package router

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"ai-cli-accounts/pkg/types"
)

// rateLimitCooldown is how long an adapter sits out after answering with a
// rate limit, before Send tries it again.
const rateLimitCooldown = 30 * time.Minute

// AccountPoolRouter dispatches a ChatRequest to the highest-priority
// adapter that isn't currently cooling down, falling over to the next one
// whenever an adapter errors (with a 30-minute cooldown specifically for
// ErrRateLimitReached, per the failover policy this router implements).
type AccountPoolRouter struct {
	adapters    []types.ProviderAdapter
	preferred   string
	mu          sync.RWMutex
	cooldownMap map[string]time.Time
}

// NewAccountPoolRouter builds a router over adapters, sorted once by
// Priority() ascending (1 tried first).
func NewAccountPoolRouter(adapters []types.ProviderAdapter) *AccountPoolRouter {
	sorted := make([]types.ProviderAdapter, len(adapters))
	copy(sorted, adapters)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority() < sorted[j].Priority() })
	return &AccountPoolRouter{adapters: sorted, cooldownMap: make(map[string]time.Time)}
}

// SetPreferred sets a preferred adapter to try before all others.
func (r *AccountPoolRouter) SetPreferred(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.preferred = id
}

// Preferred returns the currently preferred adapter ID.
func (r *AccountPoolRouter) Preferred() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.preferred
}

// Send tries each adapter in priority order (or preferred adapter first),
// skipping any still in cooldown, and returns the channel of the first one
// that starts streaming successfully.
func (r *AccountPoolRouter) Send(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.RLock()
	preferredID := r.preferred
	adapters := make([]types.ProviderAdapter, len(r.adapters))
	copy(adapters, r.adapters)
	r.mu.RUnlock()

	var errs []error

	// If a preferred adapter is set, try it first
	if preferredID != "" {
		for _, a := range adapters {
			if a.ID() == preferredID {
				if !r.cooling(a.ID()) {
					ch, err := a.SendMessageStream(ctx, req)
					if err == nil {
						return ch, nil
					}
					if errors.Is(err, types.ErrRateLimitReached) {
						r.setCooldown(a.ID())
					}
					log.Printf("router: preferred adapter %s failed: %v", a.ID(), err)
					errs = append(errs, fmt.Errorf("%s: %w", a.ID(), err))
				}
				break
			}
		}
	}

	// Normal priority order for all remaining adapters
	for _, a := range adapters {
		if a.ID() == preferredID {
			continue // already tried above
		}
		if r.cooling(a.ID()) {
			continue
		}
		ch, err := a.SendMessageStream(ctx, req)
		if err == nil {
			return ch, nil
		}
		if errors.Is(err, types.ErrRateLimitReached) {
			r.setCooldown(a.ID())
		}
		log.Printf("router: %s failed, trying next adapter: %v", a.ID(), err)
		errs = append(errs, fmt.Errorf("%s: %w", a.ID(), err))
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("router: no adapters configured, or all in cooldown")
	}
	return nil, errors.Join(errs...)
}

func (r *AccountPoolRouter) cooling(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cd, exists := r.cooldownMap[id]
	if !exists {
		return false
	}
	return time.Now().Before(cd)
}

func (r *AccountPoolRouter) setCooldown(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cooldownMap[id] = time.Now().Add(rateLimitCooldown)
}

// Status returns a summary map of each adapter, its priority, cooldown, and whether it is preferred.
func (r *AccountPoolRouter) Status() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	res := make([]map[string]any, 0, len(r.adapters))
	for _, a := range r.adapters {
		cd := r.cooldownMap[a.ID()]
		cooling := time.Now().Before(cd)
		m := map[string]any{
			"id":        a.ID(),
			"priority":  a.Priority(),
			"cooling":   cooling,
			"preferred": a.ID() == r.preferred,
		}
		if cooling {
			m["cooldown_until"] = cd.Format(time.RFC3339)
		}
		res = append(res, m)
	}
	return res
}

// Reload updates the router's adapters list and keeps existing cooldowns.
func (r *AccountPoolRouter) Reload(adapters []types.ProviderAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sorted := make([]types.ProviderAdapter, len(adapters))
	copy(sorted, adapters)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority() < sorted[j].Priority() })
	r.adapters = sorted
}
