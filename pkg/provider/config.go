package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ai-cli-accounts/pkg/types"
)

type ProviderConfig struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // "openai_compatible" | "duckduckgo" | "chatgpt_web" | "claude_web" | "gemini"
	Priority int    `json:"priority"`

	// openai_compatible & gemini
	BaseURL string `json:"baseUrl,omitempty"`
	APIKey  string `json:"apiKey,omitempty"`
	Model   string `json:"model,omitempty"`

	// chatgpt_web
	SessionToken string `json:"sessionToken,omitempty"`

	// claude_web
	SessionKey string `json:"sessionKey,omitempty"`

	// gemini_web
	Cookies string `json:"cookies,omitempty"`
}

type AccountsFile struct {
	Providers []ProviderConfig `json:"providers"`
}

func DefaultAccountsPath() string {
	return filepath.Join(types.BaseDir(), "accounts.json")
}

func ResolveSecret(val string) string {
	if strings.HasPrefix(val, "env:") {
		return os.Getenv(strings.TrimPrefix(val, "env:"))
	}
	return val
}

func BuildAdapter(p ProviderConfig) (types.ProviderAdapter, error) {
	switch p.Type {
	case "openai_compatible":
		if p.BaseURL == "" {
			return nil, fmt.Errorf("baseUrl is required")
		}
		model := p.Model
		if model == "" {
			model = "gpt-4o"
		}
		return &OpenAICompatibleAdapter{
			AdapterID:   p.ID,
			PriorityLvl: p.Priority,
			BaseURL:     p.BaseURL,
			APIKey:      ResolveSecret(p.APIKey),
			TargetModel: model,
		}, nil

	case "gemini":
		model := p.Model
		if model == "" {
			model = "gemini-2.0-flash"
		}
		return NewGeminiAdapter(p.ID, p.Priority, ResolveSecret(p.APIKey), model), nil

	case "duckduckgo":
		model := p.Model
		if model == "" {
			model = "claude-3-haiku-20240307"
		}
		return &DuckDuckGoAdapter{
			AdapterID:   p.ID,
			PriorityLvl: p.Priority,
			TargetModel: model,
		}, nil

	case "chatgpt_web":
		return &ChatGPTWebAdapter{
			AdapterID:    p.ID,
			PriorityLvl:  p.Priority,
			SessionToken: ResolveSecret(p.SessionToken),
			TargetModel:  p.Model,
		}, nil

	case "claude_web":
		return &ClaudeWebAdapter{
			AdapterID:   p.ID,
			PriorityLvl: p.Priority,
			SessionKey:  ResolveSecret(p.SessionKey),
			TargetModel: p.Model,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported provider type: %s", p.Type)
	}
}

// LoadAccounts reads accounts from accounts.json.
func LoadAccounts(path string) ([]types.ProviderAdapter, error) {
	var adapters []types.ProviderAdapter

	// 1. Load accounts.json
	b, err := os.ReadFile(path)
	if err == nil {
		var f AccountsFile
		if err := json.Unmarshal(b, &f); err == nil {
			for _, p := range f.Providers {
				a, err := BuildAdapter(p)
				if err != nil {
					continue
				}
				adapters = append(adapters, a)
			}
		}
	}

	// 2. Built-in fallback if nothing configured
	if len(adapters) == 0 {
		adapters = append(adapters, &DuckDuckGoAdapter{
			AdapterID:   "duckduckgo",
			TargetModel: "claude-3-haiku-20240307",
			PriorityLvl: 99,
		})
	}

	return adapters, nil
}

func LoadConfigFile(path string) (*AccountsFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return &AccountsFile{}, err
	}
	var f AccountsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

func SaveConfigFile(path string, f *AccountsFile) error {
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func AddOrUpdateProvider(path string, p ProviderConfig) error {
	f, err := LoadConfigFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if f == nil {
		f = &AccountsFile{}
	}

	updated := false
	for i, existing := range f.Providers {
		if existing.ID == p.ID {
			f.Providers[i] = p
			updated = true
			break
		}
	}
	if !updated {
		f.Providers = append(f.Providers, p)
	}
	return SaveConfigFile(path, f)
}

func RemoveProvider(path string, id string) error {
	f, err := LoadConfigFile(path)
	if err != nil {
		return err
	}
	var kept []ProviderConfig
	for _, p := range f.Providers {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	f.Providers = kept
	return SaveConfigFile(path, f)
}
