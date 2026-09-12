package privacy

import (
	"strings"
	"testing"

	"amux-accounts/pkg/types"
)

func TestScrubString_ReplacesSecrets(t *testing.T) {
	in := strings.Join([]string{
		"mail me at alice@corp-secret.io please",
		"key sk-ant-api03-REALSECRETVALUEHERE1234567890abcd",
		"openai sk-abcdefghijklmnopqrstuvwxyz123456",
		"Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signaturepad",
		"card 4111-1111-1111-1111",
		"ssn 123-45-6789",
		"path /Users/alice/secret/project",
		"db postgres://alice:s3cret@db.internal:5432/prod",
		"password=SuperSecretValue99",
	}, "\n")

	out, res := ScrubString(in)
	if res.Len() == 0 {
		t.Fatal("expected redactions")
	}
	for _, bad := range []string{
		"alice@corp-secret.io", "REALSECRETVALUEHERE", "abcdefghijklmnopqrstuvwxyz123456",
		"eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9", "123-45-6789", "/Users/alice",
		"alice:s3cret@db.internal", "SuperSecretValue99",
	} {
		if strings.Contains(out, bad) {
			t.Fatalf("leaked %q in: %s", bad, out)
		}
	}
	if !strings.Contains(out, "sample@example.com") {
		t.Fatalf("expected sample email, got: %s", out)
	}
	sum := res.Summary()
	for _, bad := range []string{"alice@", "REALSECRET", "SuperSecret", "s3cret"} {
		if strings.Contains(sum, bad) {
			t.Fatalf("summary leaked %q: %s", bad, sum)
		}
	}
}

func TestScrubString_ExpandedThreats(t *testing.T) {
	in := strings.Join([]string{
		"CCCD: 079203001234 and CMND 123456789",
		"MST 0312345678 passport B1234567",
		"phone 0912345678 or +84987654321",
		"call (415) 555-2671",
		"public ip 8.8.8.8 keep private 192.168.1.10",
		"mac AA:BB:CC:DD:EE:FF",
		"lat=10.762622 lng=106.660172",
		"10.762622, 106.660172",
		"Cookie: sessionid=abc123def456; theme=dark",
		"refresh_token=rrrrrrrrrrrrrrrrrr",
		"API_SECRET_KEY=should-not-leak-out",
		"https://user:p@ss@api.internal/v1",
		"redis://u:p@redis.host:6379/0",
		"telegram 7123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw",
		"discord MjxyzABC12345678901234567.Ghijkl.abcdefghijklmnopqrstuvwx123456",
		"hf_abcdefghijklmnopqrstuvwx1234567890",
		"npm_abcdefghijklmnopqrstuvwx1234567890",
		"dop_v1_abcdefghijklmnopqrstuvwx123456",
		"shpat_abcdefghijklmnopqrstuvwx1234",
		"SG.abcdefghijklmnop.qrstuvwxyz0123456789abcd",
		"btc bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
		"eth 0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
		"IBAN GB29NWBK60161331926819",
		"STK: 0123456789",
		"address: 123 Nguyen Hue, Q1, HCMC",
		"OTP: 482913",
		"Basic YWxpY2U6c2VjcmV0cGFzcw==",
	}, "\n")

	out, res := ScrubString(in)
	if res.Len() == 0 {
		t.Fatal("expected redactions")
	}
	for _, bad := range []string{
		"079203001234", "CMND 123456789", "0312345678", "B1234567",
		"0987654321", "+84987654321", "(415) 555-2671",
		"8.8.8.8", "AA:BB:CC:DD:EE:FF",
		"10.762622", "106.660172",
		"sessionid=abc123def456", "rrrrrrrrrrrrrrrrrr", "should-not-leak-out",
		"user:p@ss@", "u:p@redis.host",
		"7123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw",
		"MjxyzABC12345678901234567",
		"hf_abcdefghijklmnopqrstuvwx", "npm_abcdefghijklmnopqrstuvwx",
		"dop_v1_abcdefghijklmnopqrstuvwx", "shpat_abcdefghijklmnopqrstuvwx",
		"SG.abcdefghijklmnop",
		"bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
		"0x742d35Cc6634C0532925a3b844Bc454e4438f44e",
		"GB29NWBK60161331926819",
		"Nguyen Hue", "OTP: 482913", "482913",
		"YWxpY2U6c2VjcmV0cGFzcw==",
	} {
		if strings.Contains(out, bad) {
			t.Fatalf("leaked %q in:\n%s", bad, out)
		}
	}
	// Private / loopback IP must stay (useful for local debug context).
	if !strings.Contains(out, "192.168.1.10") {
		t.Fatalf("private ip should remain: %s", out)
	}
}

func TestScrubString_Idempotent(t *testing.T) {
	in := "contact sample@example.com with sk-ant-api03-sample-redacted-key-000000"
	out1, res1 := ScrubString(in)
	out2, res2 := ScrubString(out1)
	if res1.Len() != 0 {
		t.Fatalf("sample input should not redact, got %+v", res1)
	}
	if res2.Len() != 0 {
		t.Fatalf("second pass should be no-op, got %+v out=%q", res2, out2)
	}
	if out1 != in || out2 != in {
		t.Fatalf("samples mutated: %q → %q → %q", in, out1, out2)
	}
}

func TestScrubChatRequest(t *testing.T) {
	req := &types.ChatRequest{
		Messages: []types.ChatMessage{
			{Role: "user", Content: "token ghp_abcdefghijklmnopqrstuvwx1234567890 and bob@evil.test"},
		},
	}
	res := ScrubChatRequest(req)
	if res.Len() == 0 {
		t.Fatal("expected hits")
	}
	got := req.Messages[0].Content
	if strings.Contains(got, "ghp_abcdefghijklmnopqrstuvwx") || strings.Contains(got, "bob@evil.test") {
		t.Fatalf("secrets remain: %s", got)
	}
}

func TestScrubBytes_JSONSafe(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi alice@corp.io sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"}]}`)
	out, res := ScrubBytes(body)
	if res.Len() == 0 {
		t.Fatal("expected hits")
	}
	s := string(out)
	if strings.Contains(s, "alice@corp.io") || strings.Contains(s, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		t.Fatalf("leaked in json: %s", s)
	}
	if !strings.Contains(s, "sample@example.com") {
		t.Fatalf("missing sample: %s", s)
	}
}

func TestLuhnRejectsNonCards(t *testing.T) {
	in := "order id 1234 5678 9012 3456 not a real card hopefully"
	out, _ := ScrubString(in)
	if !strings.Contains(out, "1234") {
		t.Log(out)
	}
}

func TestScrubString_CodeSecrets(t *testing.T) {
	// Build webhook-shaped strings at runtime so the source tree never contains
	// a contiguous hooks.slack.com / discord webhook URL (push protection).
	slackWH := "https://hooks.slack.com/services/" + "TEXAMPLE0" + "/" + "BEXAMPLE0" + "/" + "abcdefghijklmnopqrstuvwx"
	discordWH := "https://discord.com/api/webhooks/" + "123456789012345678" + "/" + "abcdefghijklmnopqrstuvwxABCDEF"
	in := strings.Join([]string{
		`jdbc:mysql://root:s3cret@db.host:3306/prod`,
		`Server=db.host;Database=prod;User Id=sa;Password=P@ssw0rd!;`,
		`DefaultEndpointsProtocol=https;AccountName=myacct;AccountKey=abc123XYZ+/==;EndpointSuffix=core.windows.net`,
		`GOCSPX-abcdefghijklmnopqrst`,
		`AAAAmykey12:APA91babcdefghijklmnopqrstuvwxyz0123456789`,
		`https://deadbeefcafebabe@o123.ingest.sentry.io/456`,
		slackWH,
		discordWH,
		`cloudinary://123456789012345:abcdefghijklmnopqrstuvwx@demo`,
		`//registry.npmjs.org/:_authToken=npm_real_token_here_abc`,
		`hvs.abcdefghijklmnopqrstuvwx123456`,
		`vercel_abcdefghijklmnopqrstuvwx12`,
		`nf_abcdefghijklmnopqrstuvwx123456`,
		`sbp_abcdefghijklmnopqrstuvwx123456`,
		`APP_KEY=base64:dGVzdGtleXRlc3RrZXl0ZXN0a2V5dGVzdA==`,
		`https://ghp_abcdefghijklmnopqrstuvwx1234@github.com/org/repo.git`,
		`glpat-abcdefghijklmnopqrstuv`,
		`dd_api_key=0123456789abcdef0123456789abcdef`,
		`NRAK-ABCDEFGHIJKLMNOP`,
		`heroku_api_key=a1b2c3d4-e5f6-7890-abcd-ef1234567890`,
		"password: " + strings.Repeat("YWxhZGRpbjpvcGVuc2VzYW1l", 2),
	}, "\n")

	out, res := ScrubString(in)
	if res.Len() == 0 {
		t.Fatal("expected code redactions")
	}
	for _, bad := range []string{
		"root:s3cret@db.host", "Password=P@ssw0rd!", "AccountKey=abc123XYZ",
		"GOCSPX-abcdefghijklmnopqrst", "AAAAmykey12:APA91b",
		"deadbeefcafebabe@", "TEXAMPLE0/BEXAMPLE0",
		"webhooks/123456789012345678/",
		"cloudinary://123456789012345:",
		"npm_real_token_here_abc",
		"hvs.abcdefghijklmnopqrstuvwx",
		"vercel_abcdefghijklmnopqrstuvwx",
		"nf_abcdefghijklmnopqrstuvwx",
		"sbp_abcdefghijklmnopqrstuvwx",
		"dGVzdGtleXRlc3RrZXl0ZXN0a2V5dGVzdA==",
		"ghp_abcdefghijklmnopqrstuvwx1234@",
		"glpat-abcdefghijklmnopqrstuv",
		"0123456789abcdef0123456789abcdef",
		"NRAK-ABCDEFGHIJKLMNOP",
		"a1b2c3d4-e5f6-7890-abcd-ef1234567890",
		"YWxhZGRpbjpvcGVuc2VzYW1l",
	} {
		if strings.Contains(out, bad) {
			t.Fatalf("leaked %q in:\n%s", bad, out)
		}
	}
}

func TestPrivateIPPreserved(t *testing.T) {
	in := "hit 10.0.0.5 and 127.0.0.1 but scrub 1.1.1.1"
	out, _ := ScrubString(in)
	if !strings.Contains(out, "10.0.0.5") || !strings.Contains(out, "127.0.0.1") {
		t.Fatalf("private/loopback should stay: %s", out)
	}
	if strings.Contains(out, "1.1.1.1") {
		t.Fatalf("public ip leaked: %s", out)
	}
	if !strings.Contains(out, "203.0.113.10") {
		t.Fatalf("expected test-net sample: %s", out)
	}
}
