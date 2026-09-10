package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/sha3"
)

const (
	chatGPTSentinelURL = "https://chatgpt.com/backend-api/sentinel/chat-requirements"
	chatGPTWebUA       = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

// chatgptAccountIDFromJWT extracts chatgpt_account_id from an access-token JWT
// without verifying the signature (same trust model as the rest of the web
// adapter — the token is only ever presented back to OpenAI).
func chatgptAccountIDFromJWT(tok string) string {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return ""
	}
	seg := parts[1]
	if m := len(seg) % 4; m != 0 {
		seg += strings.Repeat("=", 4-m)
	}
	payload, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		return ""
	}
	var claims struct {
		Auth map[string]any `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Auth == nil {
		return ""
	}
	id, _ := claims.Auth["chatgpt_account_id"].(string)
	return id
}

type chatgptSentinelTokens struct {
	Requirements string
	Proof        string
}

// fetchChatGPTSentinel calls /sentinel/chat-requirements and solves the
// SHA3-512 proof-of-work challenge. Without these headers ChatGPT returns
// 403 "Unusual activity" / Cloudflare challenge HTML, which the adapter used
// to mis-report as authentication failed.
//
// PoW algorithm matches the public reverse-engineered form used by gpt4free /
// chat2api (fingerprint JSON → base64 → sha3-512(seed+base) ≤ difficulty).
func fetchChatGPTSentinel(ctx context.Context, client *http.Client, accessToken, accountID, deviceID string) (*chatgptSentinelTokens, error) {
	body := []byte(`{"p":""}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatGPTSentinelURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	setChatGPTWebHeaders(req, accessToken, accountID, deviceID, false)
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sentinel status %d: %s", resp.StatusCode, bytes.TrimSpace(raw))
	}

	var out struct {
		Token       string `json:"token"`
		ProofOfWork struct {
			Required   bool   `json:"required"`
			Seed       string `json:"seed"`
			Difficulty string `json:"difficulty"`
		} `json:"proofofwork"`
		Turnstile struct {
			Required bool `json:"required"`
		} `json:"turnstile"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode sentinel: %w", err)
	}
	if out.Token == "" {
		return nil, fmt.Errorf("sentinel: empty token")
	}

	toks := &chatgptSentinelTokens{Requirements: out.Token}
	if out.ProofOfWork.Required {
		proof, err := solveChatGPTPoW(out.ProofOfWork.Seed, out.ProofOfWork.Difficulty, chatGPTWebUA)
		if err != nil {
			return nil, err
		}
		toks.Proof = proof
	}
	return toks, nil
}

func solveChatGPTPoW(seed, difficulty, userAgent string) (string, error) {
	screens := []int{3008, 4010, 6000}
	mults := []int{1, 2, 4}
	screen := screens[rand.Intn(len(screens))] * mults[rand.Intn(len(mults))]
	parseTime := time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")
	reacts := []string{"_reactListeningcfilawjnerp", "_reactListening9ne2dfo1i47", "_reactListening410nzwhan2a"}
	wins := []string{"alert", "ontransitionend", "onprogress"}

	proof := []any{
		screen,
		parseTime,
		nil,
		0,
		userAgent,
		"https://tcr9i.chat.openai.com/v2/35536E1E-65B4-4D96-9D97-6ADB7EFF8147/api.js",
		"dpl=1440a687921de39ff5ee56b92807faaadce73f13",
		"en",
		"en-US",
		nil,
		"plugins−[object PluginArray]", // U+2212 minus, matching browser fingerprint
		reacts[rand.Intn(len(reacts))],
		wins[rand.Intn(len(wins))],
	}

	diffLen := len(difficulty)
	for i := 0; i < 500_000; i++ {
		proof[3] = i
		b, err := json.Marshal(proof)
		if err != nil {
			return "", err
		}
		base := base64.StdEncoding.EncodeToString(b)
		sum := sha3.Sum512([]byte(seed + base))
		hex := fmt.Sprintf("%x", sum[:])
		if hex[:diffLen] <= difficulty {
			return "gAAAAAB" + base, nil
		}
	}
	fallback := base64.StdEncoding.EncodeToString([]byte(`"` + seed + `"`))
	return "gAAAAABwQ8Lk5FbGpA2NcR9dShT6gYjU7VxZ4D" + fallback, nil
}

func setChatGPTWebHeaders(req *http.Request, accessToken, accountID, deviceID string, sse bool) {
	req.Header.Set("Content-Type", "application/json")
	if sse {
		req.Header.Set("Accept", "text/event-stream")
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", chatGPTWebUA)
	req.Header.Set("Origin", "https://chatgpt.com")
	req.Header.Set("Referer", "https://chatgpt.com/")
	req.Header.Set("oai-language", "en-US")
	if deviceID != "" {
		req.Header.Set("oai-device-id", deviceID)
	}
	if accountID != "" {
		req.Header.Set("chatgpt-account-id", accountID)
	}
}
