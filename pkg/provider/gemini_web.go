package provider

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"amux-accounts/pkg/types"
)

// GeminiWebAdapter talks to gemini.google.com StreamGenerate using browser
// session cookies (__Secure-1PSID + jar), not an AI Studio API key.
type GeminiWebAdapter struct {
	AdapterID   string
	PriorityLvl int
	Cookies     string
	TargetModel string
	HTTPClient  *http.Client

	mu           sync.Mutex
	cid          string
	metadataJSON string // JSON array of chat.metadata strings
	accessToken  string // SNlM0e
	buildLabel   string // cfb2h
	sessionID    string // FdrFJe
	reqID        int
	inited       bool
}

const (
	geminiWebInitURL     = "https://gemini.google.com/app"
	geminiWebGenerateURL = "https://gemini.google.com/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate"
	geminiWebUA          = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

var (
	geminiSNlM0eRe = regexp.MustCompile(`"SNlM0e"\s*:\s*"(.*?)"`)
	geminiCfb2hRe  = regexp.MustCompile(`"cfb2h"\s*:\s*"(.*?)"`)
	geminiFdrFJeRe = regexp.MustCompile(`"FdrFJe"\s*:\s*"(.*?)"`)
)

func (a *GeminiWebAdapter) ID() string    { return a.AdapterID }
func (a *GeminiWebAdapter) Priority() int { return a.PriorityLvl }

func (a *GeminiWebAdapter) client() *http.Client {
	if a.HTTPClient != nil {
		return a.HTTPClient
	}
	return defaultHTTPClient
}

func (a *GeminiWebAdapter) SendMessageStream(ctx context.Context, req *types.ChatRequest) (<-chan types.StreamChunk, error) {
	if strings.TrimSpace(a.Cookies) == "" {
		return nil, fmt.Errorf("%s: %w: no Gemini web cookies", a.AdapterID, types.ErrAuthentication)
	}
	if err := a.ensureInit(ctx); err != nil {
		return nil, err
	}

	if req.FullContext {
		a.resetConversation()
	}
	continuing := !req.FullContext && len(a.loadMetadata()) > 0
	prompt := WebBackendPrompt(req, continuing)
	meta := a.loadMetadata()
	if req.FullContext {
		meta = nil
	}

	rotated := false
	for attempt := 0; attempt < 2; attempt++ {
		text, newMeta, err := a.streamGenerate(ctx, prompt, meta)
		if err != nil {
			if isGeminiUsageLimit(err) {
				if !rotated {
					rotated = true
					a.resetConversation()
					meta = nil
					log.Printf("%s: rate/usage limit on thread — starting a new Gemini chat", a.AdapterID)
					continue
				}
				return nil, fmt.Errorf("%s: %w: %v", a.AdapterID, types.ErrRateLimitReached, err)
			}
			if isGeminiAuthErr(err) {
				return nil, fmt.Errorf("%s: %w: %v", a.AdapterID, types.ErrAuthentication, err)
			}
			return nil, fmt.Errorf("%s: %w", a.AdapterID, err)
		}
		a.persistMetadata(newMeta)
		out := make(chan types.StreamChunk, 2)
		go func() {
			defer close(out)
			sendChunk(ctx, out, types.StreamChunk{ID: a.AdapterID, Content: text})
			sendChunk(ctx, out, types.StreamChunk{ID: a.AdapterID, Done: true})
		}()
		return out, nil
	}
	return nil, types.ErrRateLimitReached
}

func (a *GeminiWebAdapter) ensureInit(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.inited && a.accessToken != "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, geminiWebInitURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", geminiWebUA)
	req.Header.Set("Cookie", a.Cookies)
	req.Header.Set("Accept", "text/html")
	resp, err := a.client().Do(req)
	if err != nil {
		return fmt.Errorf("gemini init: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	text := string(body)
	if m := geminiSNlM0eRe.FindStringSubmatch(text); len(m) > 1 {
		a.accessToken = m[1]
	}
	if m := geminiCfb2hRe.FindStringSubmatch(text); len(m) > 1 {
		a.buildLabel = m[1]
	}
	if m := geminiFdrFJeRe.FindStringSubmatch(text); len(m) > 1 {
		a.sessionID = m[1]
	}
	if a.accessToken == "" {
		if strings.Contains(text, "accounts.google.com") || resp.StatusCode == http.StatusUnauthorized {
			return types.ErrAuthentication
		}
		return fmt.Errorf("failed to extract SNlM0e — cookies may be invalid; run: am login gemini-web")
	}
	if a.reqID == 0 {
		a.reqID = 100000 + int(time.Now().UnixNano()%900000)
	}
	a.inited = true
	return nil
}

func (a *GeminiWebAdapter) loadMetadata() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.TrimSpace(a.metadataJSON) == "" {
		return nil
	}
	var meta []string
	if err := json.Unmarshal([]byte(a.metadataJSON), &meta); err != nil {
		return nil
	}
	return meta
}

func (a *GeminiWebAdapter) persistMetadata(meta []string) {
	a.mu.Lock()
	cid := ""
	if len(meta) > 0 {
		cid = meta[0]
	}
	a.cid = cid
	b, _ := json.Marshal(meta)
	a.metadataJSON = string(b)
	metaJSON := a.metadataJSON
	a.mu.Unlock()
	_ = UpdateProviderChatState(DefaultAccountsPath(), a.AdapterID, ChatState{
		ConversationID: cid,
		MetadataJSON:   metaJSON,
		ClearMetadata:  metaJSON == "" || metaJSON == "null",
	})
}

func (a *GeminiWebAdapter) resetConversation() {
	a.mu.Lock()
	a.cid = ""
	a.metadataJSON = ""
	a.mu.Unlock()
	_ = UpdateProviderChatState(DefaultAccountsPath(), a.AdapterID, ChatState{
		ClearMetadata: true,
	})
}

// ResetConversation clears the server-side Gemini thread (provider handoff).
func (a *GeminiWebAdapter) ResetConversation() { a.resetConversation() }

func (a *GeminiWebAdapter) streamGenerate(ctx context.Context, prompt string, metadata []string) (text string, newMeta []string, err error) {
	a.mu.Lock()
	at := a.accessToken
	bl := a.buildLabel
	sid := a.sessionID
	reqID := a.reqID
	a.reqID += 100000
	a.mu.Unlock()

	inner := buildGeminiStreamInner(prompt, metadata)
	innerJSON, err := json.Marshal(inner)
	if err != nil {
		return "", nil, err
	}
	outer, err := json.Marshal([]any{nil, string(innerJSON)})
	if err != nil {
		return "", nil, err
	}

	form := url.Values{}
	form.Set("at", at)
	form.Set("f.req", string(outer))

	q := url.Values{}
	q.Set("hl", "en")
	q.Set("_reqid", strconv.Itoa(reqID))
	q.Set("rt", "c")
	if bl != "" {
		q.Set("bl", bl)
	}
	if sid != "" {
		q.Set("f.sid", sid)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiWebGenerateURL+"?"+q.Encode(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", nil, err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	httpReq.Header.Set("User-Agent", geminiWebUA)
	httpReq.Header.Set("Origin", "https://gemini.google.com")
	httpReq.Header.Set("Referer", "https://gemini.google.com/")
	httpReq.Header.Set("X-Same-Domain", "1")
	httpReq.Header.Set("Cookie", a.Cookies)
	uuid := strings.ToUpper(newUUIDv4())
	httpReq.Header.Set("x-goog-ext-525005358-jspb", fmt.Sprintf(`["%s",1]`, uuid))

	resp, err := a.client().Do(httpReq)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return "", nil, types.ErrRateLimitReached
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", nil, types.ErrAuthentication
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return "", nil, fmt.Errorf("StreamGenerate status %d: %s", resp.StatusCode, bytes.TrimSpace(b))
	}

	bestText := ""
	var bestMeta []string
	errCode := 0
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line == ")]}'" {
			continue
		}
		// Length-prefixed frames: optional leading digit line, then JSON.
		if _, err := strconv.Atoi(line); err == nil {
			continue
		}
		var frames []any
		if err := json.Unmarshal([]byte(line), &frames); err != nil {
			continue
		}
		for _, fr := range frames {
			env, ok := fr.([]any)
			if !ok {
				continue
			}
			if c := geminiEnvelopeError(env); c != 0 {
				errCode = c
				continue
			}
			t, meta := geminiParseEnvelope(env)
			if len(meta) > len(bestMeta) {
				bestMeta = meta
			}
			if len(t) >= len(bestText) {
				bestText = t
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", nil, err
	}
	if bestText == "" {
		if errCode != 0 {
			return "", nil, fmt.Errorf("gemini envelope error %d", errCode)
		}
		bodyHint := ""
		return "", nil, fmt.Errorf("empty Gemini response%s", bodyHint)
	}
	return bestText, bestMeta, nil
}

func buildGeminiStreamInner(prompt string, metadata []string) []any {
	req := make([]any, 81)
	req[0] = []any{prompt, 0, nil, nil, nil, nil, 0}
	req[1] = []any{"en"}
	if len(metadata) > 0 && metadata[0] != "" {
		meta := make([]any, 10)
		for i := 0; i < 10 && i < len(metadata); i++ {
			if metadata[i] != "" {
				meta[i] = metadata[i]
			}
		}
		req[2] = meta
		req[17] = []any{[]any{1}}
	} else {
		meta := make([]any, 10)
		meta[0], meta[1], meta[2], meta[9] = "", "", "", ""
		req[2] = meta
		req[17] = []any{[]any{0}}
	}
	req[3] = "!" + geminiEntropyToken(32) // shorter than browser; enough for most accounts
	req[4] = geminiHexUUID()
	req[6] = []any{0}
	req[7] = 1
	req[10] = 1
	req[11] = 0
	req[18] = 0
	req[27] = 1
	req[30] = []any{4}
	req[41] = []any{1}
	req[53] = 0
	req[59] = strings.ToUpper(newUUIDv4())
	req[61] = []any{}
	req[68] = 1
	req[79] = 1
	req[80] = 1
	return req
}

func geminiEntropyToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func geminiHexUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func geminiEnvelopeError(envelope []any) int {
	for len(envelope) == 1 {
		inner, ok := envelope[0].([]any)
		if !ok {
			break
		}
		envelope = inner
	}
	if code := geminiDrillInt(envelope, 5, 2, 0, 1, 0); code != 0 {
		return code
	}
	if len(envelope) > 5 {
		if arr, ok := envelope[5].([]any); ok && len(arr) > 0 {
			if f, ok := arr[0].(float64); ok && f != 0 {
				return int(f)
			}
		}
	}
	return 0
}

func geminiDrillInt(arr []any, indices ...int) int {
	cur := arr
	for i, idx := range indices {
		if idx < 0 || idx >= len(cur) {
			return 0
		}
		if i == len(indices)-1 {
			if f, ok := cur[idx].(float64); ok {
				return int(f)
			}
			return 0
		}
		next, ok := cur[idx].([]any)
		if !ok {
			return 0
		}
		cur = next
	}
	return 0
}

func geminiParseEnvelope(envelope []any) (text string, meta []string) {
	for len(envelope) == 1 {
		inner, ok := envelope[0].([]any)
		if !ok {
			break
		}
		envelope = inner
	}
	if len(envelope) < 3 {
		return "", nil
	}
	contentStr, ok := envelope[2].(string)
	if !ok || contentStr == "" {
		return "", nil
	}
	var content []any
	if err := json.Unmarshal([]byte(contentStr), &content); err != nil {
		return "", nil
	}
	if len(content) > 1 {
		if metaArr, ok := content[1].([]any); ok {
			for _, v := range metaArr {
				if s, ok := v.(string); ok {
					meta = append(meta, s)
				} else {
					meta = append(meta, "")
				}
			}
		}
	}
	if len(content) > 4 {
		if candidates, ok := content[4].([]any); ok && len(candidates) > 0 {
			if cand, ok := candidates[0].([]any); ok {
				if len(cand) > 1 {
					if parts, ok := cand[1].([]any); ok && len(parts) > 0 {
						if s, ok := parts[0].(string); ok {
							text = html.UnescapeString(s)
						}
					}
				}
				if (text == "" || strings.HasPrefix(text, "http://googleusercontent.com/")) && len(cand) > 22 {
					if parts, ok := cand[22].([]any); ok && len(parts) > 0 {
						if s, ok := parts[0].(string); ok {
							text = html.UnescapeString(s)
						}
					}
				}
			}
		}
	}
	if len(content) > 25 {
		if ctx, ok := content[25].(string); ok && ctx != "" {
			for len(meta) < 10 {
				meta = append(meta, "")
			}
			meta[9] = ctx
		}
	}
	return text, meta
}

func isGeminiUsageLimit(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, types.ErrRateLimitReached) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "envelope error 1037") ||
		strings.Contains(s, "envelope error 1036") ||
		strings.Contains(s, "429")
}

func isGeminiAuthErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, types.ErrAuthentication) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "auth") || strings.Contains(s, "snlm0e")
}
