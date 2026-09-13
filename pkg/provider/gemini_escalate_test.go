package provider_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"amux-accounts/pkg/provider"
	"amux-accounts/pkg/types"
)

func TestGeminiAdapter_AutoEscalateToPro(t *testing.T) {
	var requestedModel string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		requestedModel = req.Model

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer ts.Close()

	adapter := &provider.OpenAICompatibleAdapter{
		AdapterID:   "gemini-test",
		PriorityLvl: 1,
		BaseURL:     ts.URL + "/v1beta/generativelanguage.googleapis.com", // triggers Gemini base URL logic
		TargetModel: "gemini-2.5-flash",
		HTTPClient:  ts.Client(),
	}

	// Case 1: Fast task with no thinking/pro tier
	_, err := adapter.SendMessageStream(context.Background(), &types.ChatRequest{
		Messages: []types.ChatMessage{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("SendMessageStream: %v", err)
	}
	if requestedModel != "gemini-2.5-flash" {
		t.Errorf("expected fast model gemini-2.5-flash, got %s", requestedModel)
	}

	// Case 2: Heavy task with TargetTier = pro
	_, err = adapter.SendMessageStream(context.Background(), &types.ChatRequest{
		TargetTier: "pro",
		Messages:   []types.ChatMessage{{Role: "user", Content: "Analyze architecture"}},
	})
	if err != nil {
		t.Fatalf("SendMessageStream: %v", err)
	}
	if requestedModel != provider.DefaultGeminiProModel {
		t.Errorf("expected escalated model %s, got %s", provider.DefaultGeminiProModel, requestedModel)
	}

	// Case 3: Thinking task
	_, err = adapter.SendMessageStream(context.Background(), &types.ChatRequest{
		Thinking: true,
		Messages: []types.ChatMessage{{Role: "user", Content: "Prove math theorem"}},
	})
	if err != nil {
		t.Fatalf("SendMessageStream: %v", err)
	}
	if requestedModel != provider.DefaultGeminiProModel {
		t.Errorf("expected escalated model %s, got %s", provider.DefaultGeminiProModel, requestedModel)
	}
}

func TestGeminiAdapter_NativeSwitch(t *testing.T) {
	adapter := provider.NewGeminiAdapter("gemini-native", 1, "test-key", "gemini-2.5-flash")
	if adapter.FlashModel != "gemini-2.5-flash" {
		t.Errorf("expected flashModel gemini-2.5-flash, got %s", adapter.FlashModel)
	}
	if adapter.ProModel != provider.DefaultGeminiProModel {
		t.Errorf("expected proModel %s, got %s", provider.DefaultGeminiProModel, adapter.ProModel)
	}
}
