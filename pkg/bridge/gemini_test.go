package bridge_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"amux-accounts/pkg/bridge"
	"amux-accounts/pkg/router"
	"amux-accounts/pkg/types"
)

func TestHandleGeminiGenerateContent_NonStreaming(t *testing.T) {
	adapter := &mockStreamAdapter{
		id: "gemini-mock",
		chunks: []types.StreamChunk{
			{ID: "gemini-mock", Content: "Hello from Gemini proxy!"},
			{ID: "gemini-mock", Done: true},
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{adapter})

	reqBody := `{
		"contents": [
			{
				"role": "user",
				"parts": [{"text": "Hi"}]
			}
		],
		"systemInstruction": {
			"parts": [{"text": "You are a helpful assistant"}]
		}
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", bytes.NewBufferString(reqBody))
	rec := httptest.NewRecorder()

	bridge.HandleGeminiGenerateContent(rec, req, pool)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Candidates []struct {
			Content struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if len(resp.Candidates) != 1 || resp.Candidates[0].Content.Role != "model" {
		t.Fatalf("unexpected response structure: %+v", resp)
	}
	if len(resp.Candidates[0].Content.Parts) != 1 || resp.Candidates[0].Content.Parts[0].Text != "Hello from Gemini proxy!" {
		t.Fatalf("unexpected content parts: %+v", resp.Candidates[0].Content.Parts)
	}
}

func TestHandleGeminiGenerateContent_ToolCalling(t *testing.T) {
	adapter := &mockStreamAdapter{
		id: "gemini-mock-tool",
		chunks: []types.StreamChunk{
			{
				ID: "gemini-mock-tool",
				ToolCalls: []types.ToolCall{
					{
						ID:        "call_read_1",
						Name:      "Read",
						Arguments: `{"file_path":"README.md"}`,
					},
				},
				Done: true,
			},
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{adapter})

	reqBody := `{
		"contents": [
			{"role": "user", "parts": [{"text": "Read the README"}]}
		],
		"tools": [
			{
				"functionDeclarations": [
					{
						"name": "Read",
						"description": "Read file",
						"parameters": {
							"type": "object",
							"properties": {
								"file_path": {"type": "string"}
							},
							"required": ["file_path"]
						}
					}
				]
			}
		]
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", bytes.NewBufferString(reqBody))
	rec := httptest.NewRecorder()

	bridge.HandleGeminiGenerateContent(rec, req, pool)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Candidates []struct {
			Content struct {
				Role  string `json:"role"`
				Parts []struct {
					FunctionCall *struct {
						Name string          `json:"name"`
						Args json.RawMessage `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if len(resp.Candidates) != 1 || len(resp.Candidates[0].Content.Parts) != 1 {
		t.Fatalf("unexpected candidates: %+v", resp)
	}
	fc := resp.Candidates[0].Content.Parts[0].FunctionCall
	if fc == nil || fc.Name != "Read" || !strings.Contains(string(fc.Args), "README.md") {
		t.Fatalf("unexpected functionCall: %+v", fc)
	}
}

func TestHandleGeminiGenerateContent_Streaming(t *testing.T) {
	adapter := &mockStreamAdapter{
		id: "gemini-mock-stream",
		chunks: []types.StreamChunk{
			{ID: "gemini-mock-stream", Content: "Stream "},
			{ID: "gemini-mock-stream", Content: "chunk"},
			{ID: "gemini-mock-stream", Done: true},
		},
	}
	pool := router.NewAccountPoolRouter([]types.ProviderAdapter{adapter})

	reqBody := `{"contents":[{"role":"user","parts":[{"text":"Stream test"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", bytes.NewBufferString(reqBody))
	rec := httptest.NewRecorder()

	bridge.HandleGeminiGenerateContent(rec, req, pool)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, "data:") || !strings.Contains(bodyStr, "Stream") {
		t.Fatalf("streaming output mismatch: %s", bodyStr)
	}
}

func TestGeminiBodyToChatRequest_ParallelFIFOToolCallMapping(t *testing.T) {
	body := `{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"functionCall": {"name": "Bash", "args": {"command": "git diff"}}},
					{"functionCall": {"name": "Bash", "args": {"command": "git status"}}}
				]
			},
			{
				"role": "user",
				"parts": [
					{"functionResponse": {"name": "Bash", "response": {"output": "diff result"}}},
					{"functionResponse": {"name": "Bash", "response": {"output": "status result"}}}
				]
			}
		]
	}`

	req, err := bridge.GeminiBodyToChatRequest("gemini-2.5-flash", false, []byte(body))
	if err != nil {
		t.Fatalf("GeminiBodyToChatRequest: %v", err)
	}

	if len(req.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(req.Messages), req.Messages)
	}

	assistantMsg := req.Messages[0]
	if len(assistantMsg.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls in assistant turn, got %d", len(assistantMsg.ToolCalls))
	}
	id1 := assistantMsg.ToolCalls[0].ID
	id2 := assistantMsg.ToolCalls[1].ID
	if id1 == id2 {
		t.Fatalf("tool call IDs must be unique: %q vs %q", id1, id2)
	}

	toolMsg1 := req.Messages[1]
	toolMsg2 := req.Messages[2]

	if toolMsg1.ToolCallID != id1 {
		t.Errorf("toolMsg1 ToolCallID = %q, want %q", toolMsg1.ToolCallID, id1)
	}
	if toolMsg2.ToolCallID != id2 {
		t.Errorf("toolMsg2 ToolCallID = %q, want %q", toolMsg2.ToolCallID, id2)
	}
}
