package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// am usage counts input/output tokens actually sent through the proxy, by
// account and by model, over a day/week/month/all window. Every
// /v1/messages response is tapped (without altering what the client
// receives — see wrapUsageCapture) and its usage numbers appended as one
// JSON line to ~/.am/usage.log. `am usage` just reads that file back and
// buckets it.

type usageEntry struct {
	Time    time.Time `json:"t"`
	Account string    `json:"account"`
	Model   string    `json:"model,omitempty"`
	Input   int       `json:"in"`
	Output  int       `json:"out"`
}

func usageLogPath() string { return filepath.Join(baseDir(), "usage.log") }

func appendUsageEntry(e usageEntry) {
	_ = os.MkdirAll(baseDir(), 0o700)
	f, err := os.OpenFile(usageLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return // best-effort: never break the proxy over logging
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
}

// wrapUsageCapture tees resp.Body through a parser that pulls out token
// counts (SSE `event: message_delta`/`message_stop` data lines, or a plain
// JSON body for non-streaming calls) and logs them once the body is fully
// drained — without buffering the whole response before it reaches the
// client, since ReverseProxy copies resp.Body to the client as it reads.
func wrapUsageCapture(resp *http.Response, account string) {
	if resp.Body == nil || resp.Request == nil || !strings.HasSuffix(resp.Request.URL.Path, "/v1/messages") {
		return
	}
	pr, pw := io.Pipe()
	orig := resp.Body
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.TeeReader(orig, pw), orig}

	go func() {
		e := usageEntry{Time: time.Now(), Account: account}
		parseUsageStream(pr, &e)
		_ = pr.CloseWithError(io.EOF)
		if e.Input > 0 || e.Output > 0 {
			appendUsageEntry(e)
		}
	}()

	// Close pw once the client (or ReverseProxy) finishes reading orig —
	// wrapped via the Closer above, so pw.Close() piggybacks on resp.Body.Close().
	closer := resp.Body.(interface {
		io.Reader
		io.Closer
	})
	resp.Body = struct {
		io.Reader
		io.Closer
	}{closer, closeBothCloser{closer, pw}}
}

type closeBothCloser struct {
	a io.Closer
	b io.Closer
}

func (c closeBothCloser) Close() error {
	e1 := c.a.Close()
	e2 := c.b.Close()
	if e1 != nil {
		return e1
	}
	return e2
}

// parseUsageStream reads either SSE (lines "data: {...}") or a single JSON
// body and fills in whatever input/output token counts it finds. Claude
// streams usage incrementally — input_tokens on message_start, output_tokens
// growing on message_delta — so the last non-zero value of each wins.
func parseUsageStream(r io.Reader, e *usageEntry) {
	br := bufio.NewReaderSize(r, 64*1024)
	first, _ := br.Peek(512)
	if bytes.HasPrefix(bytes.TrimSpace(first), []byte("{")) && !bytes.Contains(first, []byte("event:")) {
		// Non-streaming: drain and parse as one JSON object.
		b, _ := io.ReadAll(br)
		applyUsageJSON(b, e)
		return
	}
	sc := bufio.NewScanner(br)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		applyUsageJSON([]byte(payload), e)
	}
}

func applyUsageJSON(b []byte, e *usageEntry) {
	var v struct {
		Model   string `json:"model"`
		Message struct {
			Model string `json:"model"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(b, &v) != nil {
		return
	}
	if e.Model == "" {
		if v.Message.Model != "" {
			e.Model = v.Message.Model
		} else if v.Model != "" {
			e.Model = v.Model
		}
	}
	if v.Message.Usage.InputTokens > 0 {
		e.Input = v.Message.Usage.InputTokens
	}
	if v.Usage.InputTokens > 0 {
		e.Input = v.Usage.InputTokens
	}
	if v.Message.Usage.OutputTokens > 0 {
		e.Output = v.Message.Usage.OutputTokens
	}
	if v.Usage.OutputTokens > 0 {
		e.Output = v.Usage.OutputTokens
	}
}

// ---- am usage ----

type usageAgg struct {
	in, out int
	reqs    int
}

func cmdUsage(args []string) {
	period := "week"
	if len(args) > 0 {
		period = args[0]
	}
	var since time.Time
	now := time.Now()
	switch period {
	case "day", "today":
		y, m, d := now.Date()
		since = time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	case "week":
		since = now.AddDate(0, 0, -7)
	case "month":
		since = now.AddDate(0, -1, 0)
	case "all":
		since = time.Time{}
	default:
		die("am usage: [day|week|month|all]")
	}

	entries := loadUsageEntries()
	byAccount := map[string]*usageAgg{}
	byModel := map[string]*usageAgg{}
	var totalIn, totalOut, totalReqs int
	for _, e := range entries {
		if !since.IsZero() && e.Time.Before(since) {
			continue
		}
		bump(byAccount, e.Account, e)
		bump(byModel, orDash(e.Model), e)
		totalIn += e.Input
		totalOut += e.Output
		totalReqs++
	}

	label := period
	if !since.IsZero() {
		label = fmt.Sprintf("%s (since %s)", period, since.Local().Format("2006-01-02 15:04"))
	}
	fmt.Printf("token usage — %s\n\n", label)
	if totalReqs == 0 {
		fmt.Println("no requests logged in this window (see ~/.am/usage.log)")
		return
	}

	fmt.Println("by account:")
	printUsageTable(byAccount)
	fmt.Println()
	fmt.Println("by model:")
	printUsageTable(byModel)

	fmt.Println()
	fmt.Println(strings.Repeat("-", 66))
	fmt.Printf("%-30s  in %9s   out %9s   %5d req\n", "total", commas(totalIn), commas(totalOut), totalReqs)
}

func bump(m map[string]*usageAgg, key string, e usageEntry) {
	a := m[key]
	if a == nil {
		a = &usageAgg{}
		m[key] = a
	}
	a.in += e.Input
	a.out += e.Output
	a.reqs++
}

func printUsageTable(m map[string]*usageAgg) {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		a := m[n]
		fmt.Printf("  %-28s  in %9s   out %9s   %5d req\n", n, commas(a.in), commas(a.out), a.reqs)
	}
}

func loadUsageEntries() []usageEntry {
	f, err := os.Open(usageLogPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []usageEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var e usageEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

func commas(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	out := strings.Join(parts, ",")
	if neg {
		out = "-" + out
	}
	return out
}
