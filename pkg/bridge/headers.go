package bridge

import (
	"net/http"
	"strings"

	"amux-accounts/pkg/privacy"
	"amux-accounts/pkg/router"
	"amux-accounts/pkg/types"
)

// explicitProviderHeaders reads optional routing overrides:
//
//	X-Provider — pool account id (works even when removed from rotate pool)
//	X-Model    — override request model for this call
//
// Empty provider → caller keeps default Send() / current behavior.
func explicitProviderHeaders(r *http.Request, req *types.ChatRequest) (providerID string) {
	providerID = strings.TrimSpace(r.Header.Get("X-Provider"))
	if providerID == "" {
		providerID = strings.TrimSpace(r.Header.Get("x-provider"))
	}
	model := strings.TrimSpace(r.Header.Get("X-Model"))
	if model == "" {
		model = strings.TrimSpace(r.Header.Get("x-model"))
	}
	if model != "" && req != nil {
		req.Model = model
	}
	return providerID
}

func poolSend(r *http.Request, pool *router.AccountPoolRouter, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	if privacy.Enabled {
		if res := privacy.RedactChatRequest(req); res.Len() > 0 {
			dialect := "privacy"
			if req != nil && req.ClientDialect != "" {
				dialect = req.ClientDialect
			}
			privacy.LogHits(r, res, dialect)
		}
	}
	if id := explicitProviderHeaders(r, req); id != "" {
		return pool.SendNamed(r.Context(), id, req)
	}
	return pool.Send(r.Context(), req)
}
