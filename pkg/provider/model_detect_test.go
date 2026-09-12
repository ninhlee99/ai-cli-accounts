package provider

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPickGeminiModel(t *testing.T) {
	mk := func(name string, methods ...string) struct {
		Name                       string   `json:"name"`
		SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	} {
		return struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		}{Name: name, SupportedGenerationMethods: methods}
	}

	t.Run("prefers pro over flash", func(t *testing.T) {
		resp := geminiModelsResponse{Models: []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		}{
			mk("models/gemini-3.6-flash", "generateContent"),
			mk("models/gemini-3.6-pro", "generateContent"),
		}}
		if got := pickGeminiModel(resp); got != "gemini-3.6-pro" {
			t.Fatalf("got %q, want gemini-3.6-pro", got)
		}
	})

	t.Run("falls back to flash when no pro", func(t *testing.T) {
		resp := geminiModelsResponse{Models: []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		}{
			mk("models/gemini-3.6-flash-lite", "generateContent"),
			mk("models/gemini-3.6-flash", "generateContent"),
		}}
		if got := pickGeminiModel(resp); got != "gemini-3.6-flash" {
			t.Fatalf("got %q, want gemini-3.6-flash (not the -lite variant)", got)
		}
	})

	t.Run("ignores models without generateContent", func(t *testing.T) {
		resp := geminiModelsResponse{Models: []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		}{
			mk("models/gemini-3.6-pro", "embedContent"),
			mk("models/gemini-3.6-flash", "generateContent"),
		}}
		if got := pickGeminiModel(resp); got != "gemini-3.6-flash" {
			t.Fatalf("got %q, want gemini-3.6-flash (pro doesn't support generateContent)", got)
		}
	})

	t.Run("ignores non-gemini models", func(t *testing.T) {
		resp := geminiModelsResponse{Models: []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		}{
			mk("models/text-embedding-004", "generateContent"),
		}}
		if got := pickGeminiModel(resp); got != "" {
			t.Fatalf("got %q, want empty (no gemini-* model offered)", got)
		}
	})
}

func TestDetectGeminiModel_EmptyOrEnvKey(t *testing.T) {
	if got := DetectGeminiModel(""); got != "" {
		t.Fatalf("empty key: got %q, want empty", got)
	}
	if got := DetectGeminiModel("env:GOOGLE_AI_STUDIO_KEY"); got != "" {
		t.Fatalf("env-ref key: got %q, want empty (can't call API with unresolved env placeholder)", got)
	}
}

func TestDetectGeminiModel_QueriesModelsEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "test-key" {
			http.Error(w, "missing key", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[
			{"name":"models/gemini-3.6-flash","supportedGenerationMethods":["generateContent"]},
			{"name":"models/gemini-3.6-pro","supportedGenerationMethods":["generateContent"]}
		]}`))
	}))
	defer srv.Close()

	orig := googleAIStudioModelsURL
	googleAIStudioModelsURL = srv.URL
	defer func() { googleAIStudioModelsURL = orig }()

	if got := DetectGeminiModel("test-key"); got != "gemini-3.6-pro" {
		t.Fatalf("got %q, want gemini-3.6-pro", got)
	}
}

func TestDetectGeminiModel_ServerErrorReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	orig := googleAIStudioModelsURL
	googleAIStudioModelsURL = srv.URL
	defer func() { googleAIStudioModelsURL = orig }()

	if got := DetectGeminiModel("test-key"); got != "" {
		t.Fatalf("got %q, want empty on server error (must not block login)", got)
	}
}

func TestPickClaudeWebModel(t *testing.T) {
	cases := []struct {
		name string
		caps []string
		want string
	}{
		{"free plan, no capabilities", nil, "claude-sonnet-5"},
		{"claude_pro", []string{"chat", "claude_pro"}, "claude-opus-5"},
		{"claude_max", []string{"chat", "claude_max"}, "claude-opus-5"},
		{"claude_team", []string{"claude_team"}, "claude-opus-5"},
		{"unrelated capability only", []string{"chat"}, "claude-sonnet-5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickClaudeWebModel(c.caps); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestDetectClaudeWebModel_QueriesOrganizationsEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") == "" {
			http.Error(w, "missing cookie", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"uuid":"org-1","capabilities":["chat","claude_max"]}]`))
	}))
	defer srv.Close()

	orig := claudeWebOrganizationsURL
	claudeWebOrganizationsURL = srv.URL
	defer func() { claudeWebOrganizationsURL = orig }()

	if got := DetectClaudeWebModel("sess-key", ""); got != "claude-opus-5" {
		t.Fatalf("got %q, want claude-opus-5", got)
	}
}

func TestDetectClaudeWebModel_EmptySessionReturnsEmpty(t *testing.T) {
	if got := DetectClaudeWebModel("", ""); got != "" {
		t.Fatalf("got %q, want empty (no session to authenticate with)", got)
	}
}

func TestDetectClaudeWebModel_UnauthorizedReturnsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	orig := claudeWebOrganizationsURL
	claudeWebOrganizationsURL = srv.URL
	defer func() { claudeWebOrganizationsURL = orig }()

	if got := DetectClaudeWebModel("bad-key", ""); got != "" {
		t.Fatalf("got %q, want empty on 401 (must not block login, caller falls back)", got)
	}
}
