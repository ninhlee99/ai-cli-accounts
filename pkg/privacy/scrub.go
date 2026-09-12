// Package privacy scrubs sensitive data from outbound chat payloads before
// they leave the local proxy toward Claude / third-party APIs.
//
// Goal: block account takeover, identity theft, financial fraud, stalking /
// harassment vectors, and credential leak before anything hits the network.
package privacy

import (
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"amux-accounts/pkg/monitor"
	"amux-accounts/pkg/types"
)

// Hit describes one redaction. Original secret is NEVER stored — only kind + sample.
type Hit struct {
	Kind   string // e.g. "email", "api_key"
	Sample string // replacement that was written into the payload
	Count  int    // how many times this kind was replaced in one pass
}

// Result is the outcome of a scrub pass.
type Result struct {
	Hits []Hit
}

// Len returns total replacement occurrences across kinds.
func (r Result) Len() int {
	n := 0
	for _, h := range r.Hits {
		n += h.Count
	}
	return n
}

// Kinds returns unique kind labels in encounter order.
func (r Result) Kinds() []string {
	out := make([]string, 0, len(r.Hits))
	for _, h := range r.Hits {
		out = append(out, h.Kind)
	}
	return out
}

// Summary is a short Live Flow preview (no secrets).
func (r Result) Summary() string {
	if r.Len() == 0 {
		return ""
	}
	parts := make([]string, 0, len(r.Hits))
	for _, h := range r.Hits {
		parts = append(parts, fmt.Sprintf("%s×%d→%s", h.Kind, h.Count, truncate(h.Sample, 32)))
	}
	return "privacy scrub: " + strings.Join(parts, "; ")
}

type rule struct {
	kind    string
	sample  string
	re      *regexp.Regexp
	replace func(matched string) string // nil → use sample
}

// samples that must never be treated as secrets (idempotent re-scrub).
var knownSamples = map[string]struct{}{
	"sample@example.com":            {},
	"+1-555-0100":                   {},
	"0912345678":                    {},
	"+84912345678":                  {},
	"4111111111111111":              {},
	"000-00-0000":                   {},
	"000000000000":                  {}, // CCCD sample
	"000000000":                     {}, // CMND sample
	"0000000000":                    {}, // MST sample
	"P0000000":                      {}, // passport sample
	"GB00SAMPLE0000000000":          {},
	"AKIAIOSFODNN7EXAMPLE":          {},
	"sk-ant-api03-sample-redacted-key-000000": {},
	"sk-sample-redacted-openai-key-0000000000": {},
	"sk-proj-sample-redacted-key-000000000000": {},
	"ghp_sampleRedactedGitHubToken00000000000": {},
	"gho_sampleRedactedGitHubToken00000000000": {},
	"github_pat_sample_redacted_token_0000000": {},
	"xoxb-sample-redacted-slack-token":         {},
	"AIzaSyA-sample-redacted-google-api-key00": {},
	"sk_live_sample_redacted_stripe_key_00000": {},
	"pk_live_sample_redacted_stripe_key_00000": {},
	"Bearer sample-redacted-bearer-token":      {},
	"sample-redacted-bearer-token":             {},
	"eyJhbGciOiJsample.eyJzdWIiOiJsample.sig":  {},
	"postgres://sample:sample@localhost/db":    {},
	"mysql://sample:sample@localhost/db":       {},
	"mongodb://sample:sample@localhost/db":     {},
	"redis://sample:sample@localhost:6379/0":   {},
	"amqp://sample:sample@localhost:5672/":     {},
	"https://sample:sample@example.com/":       {},
	"/Users/sample":                            {},
	"/home/sample":                             {},
	`C:\Users\sample`:                          {},
	"203.0.113.10":                             {}, // TEST-NET-3
	"2001:db8::sample":                         {},
	"00:00:00:00:00:00":                        {},
	"bc1qsampleaddress0000000000000000000":     {},
	"0xSample000000000000000000000000000000000": {},
	"1234567890:AASampleTelegramBotToken000000": {},
	"ntn_sample_notion_token_000000000000000":   {},
	"secret_sample_notion_token_0000000000000":  {},
	"lin_api_sample_linear_token_00000000000":   {},
	"hf_sampleHuggingFaceToken00000000000000":   {},
	"npm_sampleNpmToken000000000000000000000":   {},
	"pypi-sample-pypi-token-0000000000000000":   {},
	"dop_v1_sample_digitalocean_token_0000000":  {},
	"shpat_sample_shopify_token_000000000000":   {},
	"SG.sample.sendgrid.token.000000000000000":  {},
	"xoxe.sample-slack-token":                   {},
	"heroku_api_key=00000000-0000-0000-0000-000000000000": {},
	"dd_api_key=sample00000000000000000000000000":         {},
	"NRAK-SAMPLESAMPLESAMPLESAMPLE":                       {},
	"glpat-sampleGitLabToken000000000":                    {},
	"https://sample-token@github.com/org/repo.git":        {},
	"APP_KEY=base64:sampleAPPKEY000000000000000000=":      {},
	"hvs.sampleVaultToken000000000000000":                 {},
	"vercel_sample_token_000000000000000000":              {},
	"nf_sample_netlify_token_00000000000000":              {},
	"sbp_sample_supabase_service_key_0000000":             {},
	"//registry.npmjs.org/:_authToken=sample-npm-token":   {},
	"_authToken=sample-npm-token":                         {},
	"cloudinary://sample:sample@sample":                   {},
	"https://hooks.slack.com/services/T00/B00/sample":     {},
	"https://discord.com/api/webhooks/0/sample":           {},
	"https://sample@o0.ingest.sentry.io/0":                {},
	"GOCSPX-sample-oauth-client-secret":                   {},
	"AAAAsample:APA91bsample-firebase-server-key-000000":  {},
	"jdbc:mysql://sample:sample@localhost:3306/db":        {},
	"Server=localhost;Database=sample;User Id=sample;Password=sample;": {},
	"DefaultEndpointsProtocol=https;AccountName=sample;AccountKey=sample;EndpointSuffix=core.windows.net": {},
	"password: c2FtcGxl":          {},
	"lat=0.000000":                {},
	"lng=0.000000":                {},
	"otp=000000":                  {},
	"session=sample-session-id":   {},
	"cookie=sample=sample-cookie": {},
}

var rules []rule

func init() {
	rules = []rule{
		// ── Cryptographic material / account takeover ──────────────────
		{
			kind:   "private_key",
			sample: "-----BEGIN PRIVATE KEY-----\nSAMPLE\n-----END PRIVATE KEY-----",
			re:     regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----[\s\S]*?-----END (?:RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----`),
		},
		{
			kind:   "aws_access_key",
			sample: "AKIAIOSFODNN7EXAMPLE",
			re:     regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
		},
		{
			kind:   "aws_secret_key",
			sample: "aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			re:     regexp.MustCompile(`(?i)\b(?:aws_?secret(?:_?access)?_?key|secret_?access_?key|aws_?session_?token)\s*[=:]\s*['"]?[A-Za-z0-9/+=]{20,}['"]?`),
			replace: func(string) string {
				return "aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
			},
		},
		{
			kind:   "anthropic_key",
			sample: "sk-ant-api03-sample-redacted-key-000000",
			re:     regexp.MustCompile(`\bsk-ant-[A-Za-z0-9\-_]{20,}\b`),
		},
		{
			kind:   "openai_key",
			sample: "sk-proj-sample-redacted-key-000000000000",
			re:     regexp.MustCompile(`\bsk-proj-[A-Za-z0-9\-_]{20,}\b`),
		},
		{
			kind:   "openai_key",
			sample: "sk-sample-redacted-openai-key-0000000000",
			re:     regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`),
		},
		{
			kind:   "github_token",
			sample: "github_pat_sample_redacted_token_0000000",
			re:     regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
		},
		{
			kind:   "github_token",
			sample: "ghp_sampleRedactedGitHubToken00000000000",
			re:     regexp.MustCompile(`\bgh[po]_[A-Za-z0-9]{20,}\b`),
		},
		{
			kind:   "github_token",
			sample: "ghs_sampleRedactedGitHubToken00000000000",
			re:     regexp.MustCompile(`\bghs_[A-Za-z0-9]{20,}\b`),
		},
		{
			kind:   "slack_token",
			sample: "xoxb-sample-redacted-slack-token",
			re:     regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`),
		},
		{
			kind:   "slack_token",
			sample: "xoxe.sample-slack-token",
			re:     regexp.MustCompile(`\bxoxe(?:\.[A-Za-z0-9-]+){2,}\b`),
		},
		{
			kind:   "google_api_key",
			sample: "AIzaSyA-sample-redacted-google-api-key00",
			re:     regexp.MustCompile(`\bAIza[0-9A-Za-z\-_]{35}\b`),
		},
		{
			kind:   "stripe_key",
			sample: "sk_live_sample_redacted_stripe_key_00000",
			re:     regexp.MustCompile(`\b(?:sk|pk|rk|whsec)_(?:live|test)_[A-Za-z0-9]{16,}\b`),
		},
		{
			kind:   "discord_token",
			sample: "sampleDiscordToken.XXXXXX.sampleDiscordTokenXXXXXXXXXXXX",
			re:     regexp.MustCompile(`\b[MN][A-Za-z0-9_-]{23,}\.[A-Za-z0-9_-]{6}\.[A-Za-z0-9_-]{25,}\b`),
		},
		{
			kind:   "discord_token",
			sample: "mfa.sampleDiscordMfaToken000000000000000000",
			re:     regexp.MustCompile(`\bmfa\.[A-Za-z0-9_-]{80,}\b`),
		},
		{
			kind:   "telegram_bot",
			sample: "1234567890:AASampleTelegramBotToken000000",
			re:     regexp.MustCompile(`\b\d{8,10}:AA[A-Za-z0-9_-]{30,}\b`),
		},
		{
			kind:   "twilio_sid",
			sample: "ACsamplesamplesamplesamplesample",
			re:     regexp.MustCompile(`\b(?:AC|SK)[0-9a-fA-F]{32}\b`),
		},
		{
			kind:   "sendgrid_key",
			sample: "SG.sample.sendgrid.token.000000000000000",
			re:     regexp.MustCompile(`\bSG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}\b`),
		},
		{
			kind:   "notion_token",
			sample: "ntn_sample_notion_token_000000000000000",
			re:     regexp.MustCompile(`\b(?:ntn|secret)_[A-Za-z0-9]{30,}\b`),
		},
		{
			kind:   "linear_token",
			sample: "lin_api_sample_linear_token_00000000000",
			re:     regexp.MustCompile(`\blin_api_[A-Za-z0-9]{20,}\b`),
		},
		{
			kind:   "huggingface_token",
			sample: "hf_sampleHuggingFaceToken00000000000000",
			re:     regexp.MustCompile(`\bhf_[A-Za-z0-9]{20,}\b`),
		},
		{
			kind:   "npm_token",
			sample: "npm_sampleNpmToken000000000000000000000",
			re:     regexp.MustCompile(`\bnpm_[A-Za-z0-9]{20,}\b`),
		},
		{
			kind:   "pypi_token",
			sample: "pypi-sample-pypi-token-0000000000000000",
			re:     regexp.MustCompile(`\bpypi-[A-Za-z0-9_-]{20,}\b`),
		},
		{
			kind:   "digitalocean_token",
			sample: "dop_v1_sample_digitalocean_token_0000000",
			re:     regexp.MustCompile(`\bdop_v1_[A-Za-z0-9]{20,}\b`),
		},
		{
			kind:   "shopify_token",
			sample: "shpat_sample_shopify_token_000000000000",
			re:     regexp.MustCompile(`\bshp(?:at|ca|ss)_[A-Za-z0-9]{20,}\b`),
		},
		{
			kind:   "jwt",
			sample: "eyJhbGciOiJsample.eyJzdWIiOiJsample.sig",
			re:     regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
		},
		{
			kind:   "bearer_token",
			sample: "Bearer sample-redacted-bearer-token",
			re:     regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9\-._~+/]+=*`),
		},
		{
			kind:   "basic_auth_header",
			sample: "Basic c2FtcGxlOnNhbXBsZQ==",
			re:     regexp.MustCompile(`(?i)\bBasic\s+[A-Za-z0-9+/=]{8,}`),
		},

		// ── Session / cookie / env secrets (hijack) ────────────────────
		{
			kind:   "cookie_header",
			sample: "cookie=sample=sample-cookie",
			re:     regexp.MustCompile(`(?i)\b(?:cookie|set-cookie)\s*[:=]\s*[^\n\r]+`),
			replace: func(string) string { return "cookie=sample=sample-cookie" },
		},
		{
			kind:   "session_id",
			sample: "session=sample-session-id",
			re:     regexp.MustCompile(`(?i)\b(?:session(?:id|_id)?|sess|sid|phpsessid|jsessionid|connect\.sid|csrf(?:[_-]?token)?|xsrf(?:[_-]?token)?|refresh[_-]?token|id[_-]?token|access[_-]?token|client[_-]?secret|auth[_-]?token)\s*[=:]\s*[^\s;,&"']{6,}`),
			replace: func(m string) string {
				parts := regexp.MustCompile(`(?i)^([\w.-]+)\s*([=:]).+$`).FindStringSubmatch(m)
				if len(parts) == 3 {
					return parts[1] + parts[2] + "sample-session-id"
				}
				return "session=sample-session-id"
			},
		},
		{
			kind:   "env_secret",
			sample: "SECRET_KEY=sample-secret",
			re:     regexp.MustCompile(`(?i)\b[A-Za-z_][A-Za-z0-9_]*(?:secret|token|password|passwd|pass|credentials?|api[_-]?key|private[_-]?key|auth[_-]?key)[A-Za-z0-9_]*\s*[=:]\s*[^\s"'\\]{4,}`),
			replace: func(m string) string {
				parts := regexp.MustCompile(`(?i)^([\w-]+)\s*([=:]).+$`).FindStringSubmatch(m)
				if len(parts) == 3 {
					return parts[1] + parts[2] + "sample-secret"
				}
				return "SECRET_KEY=sample-secret"
			},
		},
		{
			kind:   "password_assign",
			sample: "password=sample-password",
			re:     regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|api[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret|private[_-]?key)\s*[=:]\s*([^\s"'\\,]{4,})`),
			replace: func(m string) string {
				parts := regexp.MustCompile(`(?i)^(\w+)\s*([=:]).+$`).FindStringSubmatch(m)
				if len(parts) == 3 {
					return parts[1] + parts[2] + "sample-password"
				}
				return "password=sample-password"
			},
		},

		// ── Connection strings / URL credentials ───────────────────────
		{
			kind:   "url_credentials",
			sample: "https://sample:sample@example.com/",
			re:     regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^:\s/"']+:[^@\s/"']+@[^\s"'<>]+`),
			replace: func(m string) string {
				low := strings.ToLower(m)
				switch {
				case strings.HasPrefix(low, "postgres"):
					return "postgres://sample:sample@localhost/db"
				case strings.HasPrefix(low, "mysql"):
					return "mysql://sample:sample@localhost/db"
				case strings.HasPrefix(low, "mongo"):
					return "mongodb://sample:sample@localhost/db"
				case strings.HasPrefix(low, "redis"):
					return "redis://sample:sample@localhost:6379/0"
				case strings.HasPrefix(low, "amqp"):
					return "amqp://sample:sample@localhost:5672/"
				default:
					return "https://sample:sample@example.com/"
				}
			},
		},
		{
			kind:   "db_url",
			sample: "postgres://sample:sample@localhost/db",
			re:     regexp.MustCompile(`(?i)\b(?:postgres|postgresql|mysql|mongodb(?:\+srv)?|redis|rediss|amqp|amqps)://[^\s"'<>]+`),
			replace: func(m string) string {
				low := strings.ToLower(m)
				switch {
				case strings.HasPrefix(low, "mysql"):
					return "mysql://sample:sample@localhost/db"
				case strings.HasPrefix(low, "mongo"):
					return "mongodb://sample:sample@localhost/db"
				case strings.HasPrefix(low, "redis"):
					return "redis://sample:sample@localhost:6379/0"
				case strings.HasPrefix(low, "amqp"):
					return "amqp://sample:sample@localhost:5672/"
				default:
					return "postgres://sample:sample@localhost/db"
				}
			},
		},

		// ── Code / config / CI secrets (Claude Code paste risk) ─────────
		{
			kind:   "jdbc_url",
			sample: "jdbc:mysql://sample:sample@localhost:3306/db",
			re:     regexp.MustCompile(`(?i)\bjdbc:(?:mysql|postgresql|sqlserver|oracle|mariadb):[^\s"'<>]+`),
			replace: func(m string) string {
				low := strings.ToLower(m)
				switch {
				case strings.Contains(low, "postgres"):
					return "jdbc:postgresql://sample:sample@localhost:5432/db"
				case strings.Contains(low, "sqlserver"):
					return "jdbc:sqlserver://localhost:1433;database=sample;user=sample;password=sample"
				default:
					return "jdbc:mysql://sample:sample@localhost:3306/db"
				}
			},
		},
		{
			kind:   "ado_connection",
			sample: "Server=localhost;Database=sample;User Id=sample;Password=sample;",
			re:     regexp.MustCompile(`(?i)\b(?:Server|Data\s*Source)\s*=\s*[^;]+;[^\"'\n]{0,200}(?:Password|Pwd)\s*=\s*[^;]+`),
			replace: func(string) string {
				return "Server=localhost;Database=sample;User Id=sample;Password=sample;"
			},
		},
		{
			kind:   "azure_conn",
			sample: "DefaultEndpointsProtocol=https;AccountName=sample;AccountKey=sample;EndpointSuffix=core.windows.net",
			re:     regexp.MustCompile(`(?i)\bDefaultEndpointsProtocol\s*=\s*https?;[^\n\r]{20,}`),
			replace: func(string) string {
				return "DefaultEndpointsProtocol=https;AccountName=sample;AccountKey=sample;EndpointSuffix=core.windows.net"
			},
		},
		{
			kind:   "google_oauth_secret",
			sample: "GOCSPX-sample-oauth-client-secret",
			re:     regexp.MustCompile(`\bGOCSPX-[A-Za-z0-9_-]{10,}\b`),
		},
		{
			kind:   "firebase_server_key",
			sample: "AAAAsample:APA91bsample-firebase-server-key-000000",
			re:     regexp.MustCompile(`\bAAAA[A-Za-z0-9_-]{7,}:APA91[A-Za-z0-9_-]{20,}\b`),
		},
		{
			kind:   "sentry_dsn",
			sample: "https://sample@o0.ingest.sentry.io/0",
			re:     regexp.MustCompile(`(?i)\bhttps?://[a-f0-9]+@(?:o\d+\.)?ingest\.[^/\s]+/\d+`),
			replace: func(string) string { return "https://sample@o0.ingest.sentry.io/0" },
		},
		{
			kind:   "slack_webhook",
			sample: "https://hooks.slack.com/services/T00/B00/sample",
			re:     regexp.MustCompile(`(?i)\bhttps?://hooks\.slack\.com/services/[A-Za-z0-9+/]{8,}/[A-Za-z0-9+/]{8,}/[A-Za-z0-9+/]{8,}`),
		},
		{
			kind:   "discord_webhook",
			sample: "https://discord.com/api/webhooks/0/sample",
			re:     regexp.MustCompile(`(?i)\bhttps?://(?:discord|discordapp)\.com/api/webhooks/\d+/[A-Za-z0-9_-]{20,}`),
		},
		{
			kind:   "cloudinary_url",
			sample: "cloudinary://sample:sample@sample",
			re:     regexp.MustCompile(`(?i)\bcloudinary://[^\s"'<>]+`),
		},
		{
			kind:   "npmrc_token",
			sample: "//registry.npmjs.org/:_authToken=sample-npm-token",
			re:     regexp.MustCompile(`(?i)(?://[^\s"'=]+/:)?_authToken\s*=\s*[^\s"'\\]+`),
			replace: func(m string) string {
				if strings.Contains(m, "//") {
					return "//registry.npmjs.org/:_authToken=sample-npm-token"
				}
				return "_authToken=sample-npm-token"
			},
		},
		{
			kind:   "vault_token",
			sample: "hvs.sampleVaultToken000000000000000",
			re:     regexp.MustCompile(`\bhvs\.[A-Za-z0-9_-]{20,}\b`),
		},
		{
			kind:   "vercel_token",
			sample: "vercel_sample_token_000000000000000000",
			re:     regexp.MustCompile(`\bvercel_[A-Za-z0-9]{20,}\b`),
		},
		{
			kind:   "netlify_token",
			sample: "nf_sample_netlify_token_00000000000000",
			re:     regexp.MustCompile(`\bnf_[A-Za-z0-9]{20,}\b`),
		},
		{
			kind:   "supabase_key",
			sample: "sbp_sample_supabase_service_key_0000000",
			re:     regexp.MustCompile(`\bsb[a-z]_[A-Za-z0-9]{20,}\b`),
		},
		{
			kind:   "app_key_base64",
			sample: "APP_KEY=base64:sampleAPPKEY000000000000000000=",
			re:     regexp.MustCompile(`(?i)\b(?:APP_KEY|SECRET_KEY_BASE|SECRET_KEY)\s*=\s*base64:[A-Za-z0-9+/=]{16,}`),
			replace: func(m string) string {
				low := strings.ToLower(m)
				switch {
				case strings.Contains(low, "secret_key_base"):
					return "SECRET_KEY_BASE=base64:sampleAPPKEY000000000000000000="
				case strings.HasPrefix(low, "secret_key"):
					return "SECRET_KEY=base64:sampleAPPKEY000000000000000000="
				default:
					return "APP_KEY=base64:sampleAPPKEY000000000000000000="
				}
			},
		},
		{
			kind:   "git_token_url",
			sample: "https://sample-token@github.com/org/repo.git",
			re:     regexp.MustCompile(`(?i)\bhttps?://(?:ghp_|gho_|ghs_|github_pat_|glpat-|x-access-token:)[^@\s/'"]+@[^\s"'<>]+`),
			replace: func(m string) string {
				low := strings.ToLower(m)
				switch {
				case strings.Contains(low, "gitlab"):
					return "https://sample-token@gitlab.com/org/repo.git"
				case strings.Contains(low, "bitbucket"):
					return "https://sample-token@bitbucket.org/org/repo.git"
				default:
					return "https://sample-token@github.com/org/repo.git"
				}
			},
		},
		{
			kind:   "gitlab_token",
			sample: "glpat-sampleGitLabToken000000000",
			re:     regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{16,}\b`),
		},
		{
			kind:   "datadog_key",
			sample: "dd_api_key=sample00000000000000000000000000",
			re:     regexp.MustCompile(`(?i)\b(?:dd_api_key|datadog[_-]?api[_-]?key)\s*[=:]\s*[a-f0-9]{32}\b`),
			replace: func(string) string { return "dd_api_key=sample00000000000000000000000000" },
		},
		{
			kind:   "newrelic_key",
			sample: "NRAK-SAMPLESAMPLESAMPLESAMPLE",
			re:     regexp.MustCompile(`\bNRAK-[A-Z0-9]{16,}\b`),
		},
		{
			kind:   "heroku_key",
			sample: "heroku_api_key=00000000-0000-0000-0000-000000000000",
			re:     regexp.MustCompile(`(?i)\b(?:heroku[_-]?(?:api[_-]?)?key)\s*[=:]\s*[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`),
			replace: func(string) string {
				return "heroku_api_key=00000000-0000-0000-0000-000000000000"
			},
		},
		{
			kind:   "k8s_secret_yaml",
			sample: "password: c2FtcGxl",
			re:     regexp.MustCompile(`(?i)(?:^|[\n\r])[ \t]*(?:password|token|secret|apikey|api[_-]?key):\s*[A-Za-z0-9+/=]{20,}`),
			replace: func(m string) string {
				// Preserve leading newline / indent; only rewrite value.
				idx := strings.LastIndex(m, ":")
				if idx < 0 {
					return "password: c2FtcGxl"
				}
				return m[:idx+1] + " c2FtcGxl"
			},
		},

		// ── Identity / harassment / stalking vectors ───────────────────
		{
			kind:   "email",
			sample: "sample@example.com",
			re:     regexp.MustCompile(`\b[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}\b`),
		},
		{
			kind:   "phone_vn",
			sample: "0912345678",
			re:     regexp.MustCompile(`\b(?:\+?84|0)(?:3|5|7|8|9)\d{8}\b`),
			replace: func(m string) string {
				if strings.HasPrefix(m, "+") || strings.HasPrefix(m, "84") {
					return "+84912345678"
				}
				return "0912345678"
			},
		},
		{
			kind:   "phone_e164",
			sample: "+1-555-0100",
			re:     regexp.MustCompile(`\+\d{10,15}\b`),
			replace: func(m string) string {
				if isSample(m) || m == "+84912345678" {
					return m
				}
				return "+1-555-0100"
			},
		},
		{
			kind:   "phone_us",
			sample: "+1-555-0100",
			re:     regexp.MustCompile(`\(\d{3}\)\s*\d{3}[-.\s]?\d{4}`),
		},
		{
			kind:   "cccd",
			sample: "CCCD:000000000000",
			re:     regexp.MustCompile(`(?i)\b(?:cccd|căn\s*cước(?:\s*công\s*dân)?|so\s*cccd|số\s*cccd)\s*[:#\-]?\s*\d{12}\b`),
			replace: func(string) string { return "CCCD:000000000000" },
		},
		{
			kind:   "cmnd",
			sample: "CMND:000000000",
			re:     regexp.MustCompile(`(?i)\b(?:cmnd|chứng\s*minh(?:\s*nhân\s*dân)?|so\s*cmnd|số\s*cmnd)\s*[:#\-]?\s*\d{9,12}\b`),
			replace: func(string) string { return "CMND:000000000" },
		},
		{
			kind:   "mst",
			sample: "MST:0000000000",
			re:     regexp.MustCompile(`(?i)\b(?:mst|mã\s*số\s*thuế|ma\s*so\s*thue|tax\s*id|tax\s*code)\s*[:#\-]?\s*\d{10}(?:\d{3})?\b`),
			replace: func(string) string { return "MST:0000000000" },
		},
		{
			kind:   "passport",
			sample: "passport:P0000000",
			re:     regexp.MustCompile(`(?i)\b(?:passport|hộ\s*chiếu|ho\s*chieu)\s*(?:no\.?|number|số)?\s*[:#\-]?\s*[A-Z0-9]{6,12}\b`),
			replace: func(string) string { return "passport:P0000000" },
		},
		{
			kind:   "ssn",
			sample: "000-00-0000",
			re:     regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
		},
		{
			kind:   "iban",
			sample: "GB00SAMPLE0000000000",
			re:     regexp.MustCompile(`\b[A-Z]{2}\d{2}[A-Z0-9]{10,30}\b`),
			replace: func(m string) string {
				// Avoid eating short uppercase words: IBAN usually ≥15 chars.
				if len(m) < 15 || isSample(m) {
					return m
				}
				return "GB00SAMPLE0000000000"
			},
		},
		{
			kind:   "bank_account",
			sample: "bank_account:0000000000",
			re:     regexp.MustCompile(`(?i)\b(?:bank\s*(?:account|acct)|stk|số\s*tài\s*khoản|so\s*tai\s*khoan|account\s*(?:number|no\.?))\s*[:#\-]?\s*\d{6,20}\b`),
			replace: func(string) string { return "bank_account:0000000000" },
		},
		{
			kind:   "credit_card",
			sample: "4111111111111111",
			re:     regexp.MustCompile(`\b(?:\d[ -]*?){13,19}\b`),
			replace: func(m string) string {
				digits := digitsOnly(m)
				if len(digits) < 13 || len(digits) > 19 || !luhnOK(digits) {
					return m
				}
				if _, ok := knownSamples[digits]; ok {
					return m
				}
				return "4111111111111111"
			},
		},
		{
			kind:   "btc_address",
			sample: "bc1qsampleaddress0000000000000000000",
			re:     regexp.MustCompile(`\b(?:bc1[a-z0-9]{25,90}|[13][a-km-zA-HJ-NP-Z1-9]{25,34})\b`),
			replace: func(m string) string {
				if isSample(m) || strings.Contains(strings.ToLower(m), "sample") {
					return m
				}
				return "bc1qsampleaddress0000000000000000000"
			},
		},
		{
			kind:   "eth_address",
			sample: "0xSample000000000000000000000000000000000",
			re:     regexp.MustCompile(`\b0x[a-fA-F0-9]{40}\b`),
			replace: func(m string) string {
				if strings.Contains(strings.ToLower(m), "sample") {
					return m
				}
				return "0xSample000000000000000000000000000000000"
			},
		},

		// ── Network / device (stalking, doxxing) ───────────────────────
		{
			kind:   "ipv4",
			sample: "203.0.113.10",
			re:     regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|[01]?\d\d?)\.){3}(?:25[0-5]|2[0-4]\d|[01]?\d\d?)\b`),
			replace: func(m string) string {
				if isSample(m) || isNonPublicIPv4(m) {
					return m
				}
				return "203.0.113.10"
			},
		},
		{
			kind:   "ipv6",
			sample: "2001:db8::sample",
			re:     regexp.MustCompile(`\b(?:[0-9a-fA-F]{1,4}:){2,7}[0-9a-fA-F]{0,4}\b`),
			replace: func(m string) string {
				if isSample(m) || m == "::1" || strings.HasPrefix(strings.ToLower(m), "fe80:") || strings.HasPrefix(strings.ToLower(m), "fc") || strings.HasPrefix(strings.ToLower(m), "fd") {
					return m
				}
				// Avoid eating timestamps / versions that look vaguely like ipv6 fragments.
				if strings.Count(m, ":") < 2 {
					return m
				}
				return "2001:db8::sample"
			},
		},
		{
			kind:   "mac_address",
			sample: "00:00:00:00:00:00",
			re:     regexp.MustCompile(`\b(?:[0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}\b`),
		},
		{
			kind:   "geo_coord",
			sample: "lat=0.000000 lng=0.000000",
			re:     regexp.MustCompile(`(?i)\b(?:lat(?:itude)?|long(?:itude)?|lng)\s*[=:]\s*-?\d{1,3}\.\d{2,}`),
			replace: func(m string) string {
				low := strings.ToLower(m)
				if strings.Contains(low, "long") || strings.Contains(low, "lng") {
					return "lng=0.000000"
				}
				return "lat=0.000000"
			},
		},
		{
			kind:   "geo_pair",
			sample: "0.000000,0.000000",
			re:     regexp.MustCompile(`\b-?\d{1,2}\.\d{4,},\s*-?\d{1,3}\.\d{4,}\b`),
		},
		{
			kind:   "home_path",
			sample: "/Users/sample",
			re:     regexp.MustCompile(`(?i)(?:/Users|/home)/[^/\s"'\\]+`),
			replace: func(m string) string {
				if strings.HasPrefix(strings.ToLower(m), "/home/") {
					return "/home/sample"
				}
				return "/Users/sample"
			},
		},
		{
			kind:   "home_path",
			sample: `C:\Users\sample`,
			re:     regexp.MustCompile(`(?i)C:\\Users\\[^\\\s"']+`),
		},
		{
			kind:   "address_label",
			sample: "address:123 Sample Street",
			re:     regexp.MustCompile(`(?i)\b(?:address|địa\s*chỉ|dia\s*chi|home\s*address|shipping\s*address)\s*[=:]\s*[^\n\r]{8,120}`),
			replace: func(string) string { return "address:123 Sample Street" },
		},
		{
			kind:   "otp_code",
			sample: "otp=000000",
			re:     regexp.MustCompile(`(?i)\b(?:otp|mã\s*otp|ma\s*otp|verification\s*code|auth(?:entication)?\s*code|2fa\s*code|one[-\s]?time\s*(?:pass(?:word)?|code))\s*[=:#]?\s*\d{4,8}\b`),
			replace: func(string) string { return "otp=000000" },
		},
	}
}

func isNonPublicIPv4(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return true
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return true
	}
	if ip4.IsLoopback() || ip4.IsPrivate() || ip4.IsLinkLocalUnicast() || ip4.IsLinkLocalMulticast() || ip4.IsUnspecified() {
		return true
	}
	// Documentation / benchmark ranges — treat as non-sensitive.
	if s == "203.0.113.10" || strings.HasPrefix(s, "203.0.113.") || strings.HasPrefix(s, "198.51.100.") || strings.HasPrefix(s, "192.0.2.") {
		return true
	}
	return false
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func luhnOK(digits string) bool {
	sum := 0
	alt := false
	for i := len(digits) - 1; i >= 0; i-- {
		n := int(digits[i] - '0')
		if alt {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		alt = !alt
	}
	return sum%10 == 0
}

func isSample(s string) bool {
	s = strings.TrimSpace(s)
	if _, ok := knownSamples[s]; ok {
		return true
	}
	low := strings.ToLower(s)
	if strings.Contains(low, "sample-redacted") ||
		strings.Contains(low, "sample@example.com") ||
		strings.Contains(low, "sample-secret") ||
		strings.Contains(low, "sample-password") ||
		strings.Contains(low, "sample-session") ||
		strings.Contains(low, "sample-cookie") ||
		strings.Contains(low, ":sample") ||
		strings.Contains(low, "sampletelegram") ||
		strings.Contains(low, "samplediscord") {
		return true
	}
	if strings.HasPrefix(s, "-----BEGIN PRIVATE KEY-----") && strings.Contains(s, "SAMPLE") {
		return true
	}
	return false
}

// ScrubString replaces sensitive spans with sample placeholders.
func ScrubString(in string) (string, Result) {
	if in == "" {
		return in, Result{}
	}
	out := in
	counts := map[string]int{}
	samples := map[string]string{}
	order := []string{}

	for _, rule := range rules {
		out = rule.re.ReplaceAllStringFunc(out, func(m string) string {
			if isSample(m) {
				return m
			}
			rep := rule.sample
			if rule.replace != nil {
				rep = rule.replace(m)
			}
			if rep == m {
				return m
			}
			if counts[rule.kind] == 0 {
				order = append(order, rule.kind)
				samples[rule.kind] = rep
			}
			counts[rule.kind]++
			return rep
		})
	}

	var hits []Hit
	for _, k := range order {
		hits = append(hits, Hit{Kind: k, Sample: samples[k], Count: counts[k]})
	}
	return out, Result{Hits: hits}
}

// ScrubBytes scrubs a raw request body (JSON or plain text).
func ScrubBytes(body []byte) ([]byte, Result) {
	if len(body) == 0 {
		return body, Result{}
	}
	s, res := ScrubString(string(body))
	if res.Len() == 0 {
		return body, res
	}
	return []byte(s), res
}

// ScrubChatRequest mutates message/tool text in place.
func ScrubChatRequest(req *types.ChatRequest) Result {
	if req == nil {
		return Result{}
	}
	merged := Result{}
	add := func(r Result) {
		for _, h := range r.Hits {
			found := false
			for i := range merged.Hits {
				if merged.Hits[i].Kind == h.Kind {
					merged.Hits[i].Count += h.Count
					found = true
					break
				}
			}
			if !found {
				merged.Hits = append(merged.Hits, h)
			}
		}
	}

	for i := range req.Messages {
		m := &req.Messages[i]
		if m.Content != "" {
			s, r := ScrubString(m.Content)
			m.Content = s
			add(r)
		}
		for j := range m.ToolCalls {
			tc := &m.ToolCalls[j]
			if tc.Arguments != "" {
				s, r := ScrubString(tc.Arguments)
				tc.Arguments = s
				add(r)
			}
		}
	}
	for i := range req.Tools {
		t := &req.Tools[i]
		if t.Description != "" {
			s, r := ScrubString(t.Description)
			t.Description = s
			add(r)
		}
		if len(t.InputSchema) > 0 {
			b, r := ScrubBytes(t.InputSchema)
			t.InputSchema = b
			add(r)
		}
	}
	return merged
}

// LogHits writes a Live Flow row describing redactions (kinds + samples only).
func LogHits(r *http.Request, res Result, dialect string) {
	if res.Len() == 0 {
		return
	}
	path := ""
	if r != nil && r.URL != nil {
		path = r.URL.Path
	}
	if dialect == "" {
		dialect = "privacy"
	}
	kinds := res.Kinds()
	monitor.AppendRequest(types.RequestEntry{
		Time:       time.Now(),
		Dialect:    dialect,
		Path:       path,
		Account:    "local",
		Model:      "",
		Input:      res.Summary(),
		Output:     "blocked outbound leak · replaced with samples",
		StopReason: "privacy_scrub",
		Tools:      append([]string{"privacy"}, kinds...),
		ToolStatus: "ok",
		Redactions: kinds,
	})
	monitor.AppendEvent("PRIVACY", res.Summary())
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// MergeResults combines multiple scrub results.
func MergeResults(parts ...Result) Result {
	out := Result{}
	for _, p := range parts {
		for _, h := range p.Hits {
			found := false
			for i := range out.Hits {
				if out.Hits[i].Kind == h.Kind {
					out.Hits[i].Count += h.Count
					found = true
					break
				}
			}
			if !found {
				out.Hits = append(out.Hits, h)
			}
		}
	}
	return out
}
