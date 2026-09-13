package router

import (
	"regexp"
	"strings"

	"amux-accounts/pkg/types"
)

// TaskClassification holds the analysis result of a ChatRequest.
type TaskClassification struct {
	// IsHeavy indicates the task requires deep reasoning or complex analysis,
	// warranting escalation to a Pro tier model (e.g. gemini-3.1-pro / gemini-2.5-pro / o3-mini).
	IsHeavy bool
	// NeedsThinking indicates whether extended thinking / reasoning tokens should be activated.
	NeedsThinking bool
	// ReasoningEffort is "low", "medium", or "high".
	ReasoningEffort string
	// Reasons lists the signals triggering this classification (for logs/monitoring).
	Reasons []string
}

var (
	// Fast/trivial prompts that should not trigger heavy thinking.
	reTrivialPrompt = regexp.MustCompile(`(?i)^(?:hi|hello|hey|chào|ok|okay|yes|no|được|tiếp tục|continue|git status|pwd|ls|dir|help)\b`)

	// Keywords indicating deep analysis, architecture, or security audit.
	reHeavyKeywords = regexp.MustCompile(`(?i)\b(?:` +
		`architecture|kiến\s*trúc|system\s*design|thiết\s*kế\s*hệ\s*thống|` +
		`audit|security|bảo\s*mật|vulnerability|lỗ\s*hổng|cve|penetration|` +
		`code\s*review|đánh\s*giá\s*code|phân\s*tích\s*chuyên\s*sâu|deep\s*analysis|` +
		`root\s*cause|nguyên\s*nhân\s*gốc|race\s*condition|deadlock|memory\s*leak|` +
		`refactor|tái\s*cấu\s*trúc|tối\s*ưu\s*hiệu\s*năng|performance\s*optimization|` +
		`algorithm|thuật\s*toán|complexity|formal\s*proof|chứng\s*minh|` +
		`multi-agent|suy\s*luận\s*từng\s*bước|step\s*by\s*step\s*reasoning|` +
		`investigate\s*why|tại\s*sao\s*bị\s*lỗi` +
		`)\b`)

	// Stack trace or crash dump signatures in tool results.
	reCrashOrStackTrace = regexp.MustCompile(`(?i)(?:panic:|Traceback \(most recent call last\):|fatal error:|NullPointerException|SIGSEGV|segmentation fault)`)

	// Explicit pro / reasoning model names requested by client.
	reProModelName = regexp.MustCompile(`(?i)(?:o1|o3|gemini-(?:3\.1|2\.5|1\.5)-pro|claude-3-7-sonnet|deepseek-r1|\br1\b)`)
)

// ClassifyTask evaluates an incoming ChatRequest to determine if it needs
// Pro tier escalation and extended thinking mode.
func ClassifyTask(req *types.ChatRequest) TaskClassification {
	if req == nil {
		return TaskClassification{ReasoningEffort: "medium"}
	}

	var res TaskClassification
	res.ReasoningEffort = "medium"

	// 1. Explicit client signals.
	if req.Thinking {
		res.NeedsThinking = true
		res.Reasons = append(res.Reasons, "client requested thinking mode")
	}
	if strings.EqualFold(req.TargetTier, "pro") {
		res.IsHeavy = true
		res.Reasons = append(res.Reasons, "client requested pro tier")
	}
	if req.ReasoningEffort != "" {
		res.ReasoningEffort = req.ReasoningEffort
		res.NeedsThinking = true
	}
	if req.Model != "" && reProModelName.MatchString(req.Model) {
		res.IsHeavy = true
		res.NeedsThinking = true
		res.Reasons = append(res.Reasons, "model name specifies pro/reasoning tier: "+req.Model)
	}

	// Extract prompt texts from messages.
	var lastUserText string
	var totalLen int
	var codeBlockCount int
	var hasStackTrace bool
	var hasDiff bool

	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		totalLen += len(m.Content)
		if lastUserText == "" && strings.EqualFold(m.Role, "user") && m.Content != "" {
			lastUserText = strings.TrimSpace(m.Content)
		}
		if !hasStackTrace && reCrashOrStackTrace.MatchString(m.Content) {
			hasStackTrace = true
		}
		if !hasDiff && (strings.Contains(m.Content, "diff --git") || strings.Contains(m.Content, "@@ -")) {
			hasDiff = true
		}
		codeBlockCount += strings.Count(m.Content, "```")
	}

	// 2. Trivial prompt check — do not auto-escalate trivial greetings or quick commands
	// unless explicitly requested by the client.
	if len(lastUserText) < 50 && reTrivialPrompt.MatchString(lastUserText) && !req.Thinking && !strings.EqualFold(req.TargetTier, "pro") {
		return res
	}

	// 3. Keyword-based heavy task detection.
	keywordCount := 0
	if lastUserText != "" && reHeavyKeywords.MatchString(lastUserText) {
		res.IsHeavy = true
		res.NeedsThinking = true
		matches := reHeavyKeywords.FindAllString(lastUserText, 5)
		keywordCount = len(matches)
		res.Reasons = append(res.Reasons, "heavy task keywords: "+strings.Join(matches, ", "))
	}

	// 4. Code & context volume heuristic.
	if hasDiff && (strings.Contains(strings.ToLower(lastUserText), "review") || strings.Contains(strings.ToLower(lastUserText), "kiểm tra")) {
		res.IsHeavy = true
		res.NeedsThinking = true
		res.Reasons = append(res.Reasons, "code diff review task")
	}

	if hasStackTrace {
		res.IsHeavy = true
		res.NeedsThinking = true
		res.Reasons = append(res.Reasons, "stack trace / crash investigation")
	}

	// Long conversation context or multiple substantial code blocks (> 3000 chars of code)
	if codeBlockCount >= 4 && totalLen > 8000 {
		res.IsHeavy = true
		res.Reasons = append(res.Reasons, "large multi-turn code context")
	}

	// Calibrate reasoning effort
	if keywordCount >= 2 || len(res.Reasons) >= 2 || (res.IsHeavy && (hasStackTrace || hasDiff)) {
		res.ReasoningEffort = "high"
	}

	return res
}
