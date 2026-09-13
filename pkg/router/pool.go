package router

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"amux-accounts/pkg/privacy"
	"amux-accounts/pkg/term"
	"amux-accounts/pkg/types"
)

// redactBeforeSend is the universal outbound gate: every adapter path
// (proxy bridge, am chat, tests) must pass here before network I/O.
func redactBeforeSend(req *types.ChatRequest) {
	if !privacy.Enabled {
		return
	}
	res := privacy.RedactChatRequest(req)
	if res.Len() == 0 {
		return
	}
	dialect := "pool"
	if req != nil && req.ClientDialect != "" {
		dialect = req.ClientDialect
	}
	privacy.LogHits(nil, res, dialect)
}

// rateLimitCooldown is how long an adapter sits out after answering with a
// rate limit, before Send tries it again. Keep short: Claude/ChatGPT web
// free tiers often return brief 429s; a 30-minute sit-out made interactive
// `am chat` unusable after one burst.
const rateLimitCooldown = 2 * time.Minute

// AccountPoolRouter dispatches a ChatRequest to the highest-priority
// adapter that isn't currently cooling down. A preferred adapter from
// `am sw <provider>` is tried first; rate-limit failover promotes the
// winner to preferred so status stays in sync. Non-rate-limit failures
// on a pin do not silently jump to another provider.
//
// directory holds every addressable adapter (including out-of-pool) for
// explicit X-Provider routing via SendNamed.
type AccountPoolRouter struct {
	adapters    []types.ProviderAdapter
	directory   map[string]types.ProviderAdapter
	preferred   string
	lastUsed    string
	mu          sync.RWMutex
	cooldownMap map[string]time.Time
}

// NewAccountPoolRouter builds a router over adapters, sorted once by
// Priority() ascending (1 tried first).
func NewAccountPoolRouter(adapters []types.ProviderAdapter) *AccountPoolRouter {
	sorted := make([]types.ProviderAdapter, len(adapters))
	copy(sorted, adapters)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority() < sorted[j].Priority() })
	dir := make(map[string]types.ProviderAdapter, len(sorted))
	for _, a := range sorted {
		dir[a.ID()] = a
	}
	return &AccountPoolRouter{adapters: sorted, directory: dir, cooldownMap: make(map[string]time.Time)}
}

// SetDirectory replaces the explicit-routing map (may include adapters not
// in the rotate pool). Rotate order is unchanged.
func (r *AccountPoolRouter) SetDirectory(adapters []types.ProviderAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dir := make(map[string]types.ProviderAdapter, len(adapters))
	for _, a := range adapters {
		dir[a.ID()] = a
	}
	// Keep rotate adapters addressable too.
	for _, a := range r.adapters {
		dir[a.ID()] = a
	}
	r.directory = dir
}

// applyTaskClassification inspects the request and dynamically enables thinking
// mode or escalates to Pro tier when heavy analytical reasoning is required.
func applyTaskClassification(req *types.ChatRequest) {
	if req == nil {
		return
	}
	c := ClassifyTask(req)
	if c.IsHeavy && req.TargetTier == "" {
		req.TargetTier = "pro"
		log.Printf("router: heavy task detected (%s) -> escalated to Pro tier", strings.Join(c.Reasons, ", "))
	}
	if c.NeedsThinking && !req.Thinking {
		req.Thinking = true
		if req.ReasoningEffort == "" {
			req.ReasoningEffort = c.ReasoningEffort
		}
		if req.ThinkingBudget <= 0 {
			req.ThinkingBudget = 2048
		}
		log.Printf("router: auto-enabled thinking mode (effort: %s, budget: %d, reasons: %s)",
			req.ReasoningEffort, req.ThinkingBudget, strings.Join(c.Reasons, ", "))
	}
}

// SendNamed routes to one adapter by ID (rotate pool or out-of-pool directory).
func (r *AccountPoolRouter) SendNamed(ctx context.Context, id string, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	applyTaskClassification(req)
	redactBeforeSend(req)
	r.mu.RLock()
	a := r.directory[id]
	r.mu.RUnlock()
	if a == nil {
		return nil, fmt.Errorf("provider %q not addressable (see: am accounts)", id)
	}
	ch, err := a.SendMessageStream(ctx, req)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.lastUsed = id
	r.mu.Unlock()
	return ch, nil
}

// SetPreferred sets the active/pinned adapter shown in status and tried
// first by Send. Auto-failover also calls this so status tracks reality.
func (r *AccountPoolRouter) SetPreferred(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.preferred = id
}

// ConversationResetter is implemented by web adapters that keep a
// server-side chat thread. Called on `am sw` so the next turn does not
// continue an unrelated conversation.
type ConversationResetter interface {
	ResetConversation()
}

// ResetConversations clears server-side web threads on every adapter that
// supports it (Claude/ChatGPT/Gemini web). Safe to call when switching
// providers so Claude Code history is not mixed with an old UI chat.
func (r *AccountPoolRouter) ResetConversations() {
	r.mu.RLock()
	adapters := append([]types.ProviderAdapter(nil), r.adapters...)
	r.mu.RUnlock()
	for _, a := range adapters {
		if rr, ok := a.(ConversationResetter); ok {
			rr.ResetConversation()
		}
	}
}

// Preferred returns the currently preferred adapter ID.
func (r *AccountPoolRouter) Preferred() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.preferred
}

// LastUsed returns the adapter ID that last successfully started a stream.
func (r *AccountPoolRouter) LastUsed() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastUsed
}

// Len returns how many adapters the pool currently holds (including
// auto-surfaced Codex fallback).
func (r *AccountPoolRouter) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.adapters)
}

type toolCapability interface {
	SupportsTools() bool
}

func adapterSupportsTools(a types.ProviderAdapter) bool {
	if t, ok := a.(toolCapability); ok {
		return t.SupportsTools()
	}
	return true
}

// skipWebWhenTools: off while testing Claude Code against chatgpt/claude/gemini web.
const skipWebWhenTools = false

func skipTextOnly(a types.ProviderAdapter, req *types.ChatRequest) bool {
	return skipWebWhenTools && req != nil && len(req.Tools) > 0 && !adapterSupportsTools(a)
}

// Send tries adapters in priority order. With a preferred pin (`am sw`):
// try that adapter first; on rate-limit only, fail over and promote the
// winner to preferred so status shows the auto-switch. Other errors stay
// pinned (no silent jump to Gemini). Without a pin: normal priority
// failover, and any failover winner is promoted to preferred.
//
// Client tool loops (Claude Code / Cursor / Codex) send tools[]. Text-only
// web backends cannot emit tool_use — skip them unless the caller pinned
// via SendNamed (X-Provider).
func (r *AccountPoolRouter) Send(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	applyTaskClassification(req)
	redactBeforeSend(req)

	r.mu.RLock()
	preferredID := r.preferred
	adapters := make([]types.ProviderAdapter, len(r.adapters))
	copy(adapters, r.adapters)
	r.mu.RUnlock()

	var errs []error
	var skippedPreferred bool

	if preferredID != "" {
		for _, a := range adapters {
			if a.ID() != preferredID {
				continue
			}
			if skipTextOnly(a, req) {
				skippedPreferred = true
				errs = append(errs, fmt.Errorf("%s: skip text-only backend (client sent tools)", a.ID()))
				break
			}
			if r.cooling(a.ID()) {
				skippedPreferred = true
				errs = append(errs, fmt.Errorf("%s: cooling down", a.ID()))
				break
			}
			ch, err := a.SendMessageStream(ctx, req)
			if err == nil {
				r.markUsed(a.ID(), true)
				return ch, nil
			}
			if errors.Is(err, types.ErrRateLimitReached) {
				r.setCooldown(a.ID())
				skippedPreferred = true
				term.LogFailover("preferred %s rate-limited — failing over", a.ID())
				errs = append(errs, fmt.Errorf("%s: %w", a.ID(), err))
				break
			}
			log.Printf("router: preferred adapter %s failed (pinned, no failover): %v", a.ID(), err)
			return nil, fmt.Errorf("%s: %w", a.ID(), err)
		}
		if !skippedPreferred && len(errs) == 0 {
			return nil, fmt.Errorf("router: preferred adapter %q not in pool", preferredID)
		}
	}

	for _, a := range adapters {
		if preferredID != "" && a.ID() == preferredID {
			continue
		}
		if skipTextOnly(a, req) {
			continue
		}
		if r.cooling(a.ID()) {
			continue
		}
		ch, err := a.SendMessageStream(ctx, req)
		if err == nil {
			// Promote winner so status ● active matches who answered
			// (covers unpinned pick + rate-limit failover from a pin).
			was := preferredID
			r.markUsed(a.ID(), true)
			if was != "" && was != a.ID() {
				term.LogPool("auto-switch %s → %s", was, a.ID())
			} else if was == "" {
				term.LogPool("active provider → %s", a.ID())
			}
			return ch, nil
		}
		if errors.Is(err, types.ErrRateLimitReached) {
			r.setCooldown(a.ID())
		}
		term.LogWarn("%s failed, next: %v", a.ID(), err)
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

// markUsed records lastUsed; when promote is true also sets preferred so
// `am status` shows the auto-switched adapter as active.
func (r *AccountPoolRouter) markUsed(id string, promote bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastUsed = id
	if promote {
		r.preferred = id
	}
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
			"last_used": a.ID() == r.lastUsed,
			"in_pool":   true,
		}
		if cooling {
			m["cooldown_until"] = cd.Format(time.RFC3339)
		}
		res = append(res, m)
	}
	return res
}

// Reload updates the router's rotate-pool adapters and merges them into the
// addressable directory (keeps existing out-of-pool entries).
func (r *AccountPoolRouter) Reload(adapters []types.ProviderAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sorted := make([]types.ProviderAdapter, len(adapters))
	copy(sorted, adapters)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority() < sorted[j].Priority() })
	r.adapters = sorted
	if r.directory == nil {
		r.directory = make(map[string]types.ProviderAdapter)
	}
	for _, a := range sorted {
		r.directory[a.ID()] = a
	}
}
