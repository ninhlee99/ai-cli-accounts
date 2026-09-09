package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	Project string    `json:"project,omitempty"`
	Session string    `json:"session,omitempty"`
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
	debug := os.Getenv("AM_PROXY_DEBUG") != ""
	if resp.Body == nil || resp.Request == nil || !strings.HasSuffix(resp.Request.URL.Path, "/v1/messages") {
		if debug {
			path := ""
			if resp.Request != nil {
				path = resp.Request.URL.Path
			}
			log.Printf("usage: skip capture, path=%q bodyNil=%v", path, resp.Body == nil)
		}
		return
	}
	pr, pw := io.Pipe()
	orig := resp.Body
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.TeeReader(orig, pw), orig}

	project, session := "", ""
	if resp.Request != nil {
		project = projectForRemoteAddr(resp.Request.RemoteAddr)
		// Claude Code sends its own session UUID on every request (since
		// v2.1.86) and mints a new one on /clear (that's a new session to
		// Claude Code internally, not just a cleared context) — so grouping
		// by this header gives exactly "running total per session, resets
		// on /clear" for free, no state tracking needed on our side.
		session = resp.Request.Header.Get("X-Claude-Code-Session-Id")
	}

	gzipped := strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip")
	go func() {
		e := usageEntry{Time: time.Now(), Account: account, Project: project, Session: session}
		var src io.Reader = pr
		if gzipped {
			// Upstream sends gzip'd bodies (we forward the client's own
			// Accept-Encoding as-is, so ReverseProxy's transport never
			// auto-decompresses) — the tee is of the raw wire bytes since
			// that's what must reach the client unmodified, so decode our
			// own copy here before scanning for usage JSON.
			if gr, err := gzip.NewReader(pr); err == nil {
				defer gr.Close()
				src = gr
			} else {
				src = nil
			}
		}
		if src != nil {
			parseUsageStream(src, &e)
		} else {
			_, _ = io.Copy(io.Discard, pr) // drain so the pipe doesn't block the response
		}
		_ = pr.CloseWithError(io.EOF)
		if debug {
			log.Printf("usage: parsed in=%d out=%d model=%q account=%q status=%d gzip=%v", e.Input, e.Output, e.Model, e.Account, resp.StatusCode, gzipped)
		}
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

// cmdUsage: default output is one row per calendar day (a table), no
// breakdown — `--detail` swaps that for the account/model/project/session
// breakdown (still filtered to the period/date/project given). `--date`
// pins the window to a single calendar day instead of day/week/month/all.
// `--project` filters every mode down to one project.
func cmdUsage(args []string) {
	period := "week"
	detail := false
	var dateArg, projectFilter string
	periodGiven := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--detail", "-D":
			detail = true
		case "--date", "-d":
			i++
			if i >= len(args) {
				die("am usage: --date/-d needs a value (YYYY-MM-DD)")
			}
			dateArg = args[i]
		case "--project", "-p":
			i++
			if i >= len(args) {
				die("am usage: --project/-p needs a value")
			}
			projectFilter = args[i]
		default:
			if periodGiven || strings.HasPrefix(a, "-") {
				die("am usage: [day|week|month|all] [-d|--date YYYY-MM-DD] [-p|--project NAME] [-D|--detail]")
			}
			period = a
			periodGiven = true
		}
	}

	now := time.Now()
	var since time.Time
	var dateOnly time.Time // zero unless --date pins a single day
	if dateArg != "" {
		d, err := time.ParseInLocation("2006-01-02", dateArg, now.Location())
		if err != nil {
			die("am usage: --date %q: want YYYY-MM-DD", dateArg)
		}
		dateOnly = d
		since = d
	} else {
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
			die("am usage: [day|week|month|all] [--date YYYY-MM-DD] [--project NAME] [--detail]")
		}
	}

	entries := loadUsageEntries()
	var filtered []usageEntry
	for _, e := range entries {
		if !since.IsZero() && e.Time.Before(since) {
			continue
		}
		if !dateOnly.IsZero() && e.Time.After(dateOnly.AddDate(0, 0, 1)) {
			continue
		}
		if projectFilter != "" && projectLabel(e.Project) != projectFilter {
			continue
		}
		filtered = append(filtered, e)
	}

	label := period
	if !dateOnly.IsZero() {
		label = dateOnly.Format("2006-01-02")
	} else if !since.IsZero() {
		label = fmt.Sprintf("%s (since %s)", period, since.Local().Format("2006-01-02 15:04"))
	}
	if projectFilter != "" {
		label += "  project=" + projectFilter
	}
	fmt.Printf("token usage — %s\n\n", label)
	if len(filtered) == 0 {
		fmt.Println("no requests logged in this window (see ~/.am/usage.log)")
		return
	}

	if detail {
		printUsageDetail(filtered)
		return
	}
	printUsageByDay(filtered)
}

// printUsageByDay is the default view: one row per calendar day, oldest
// first, plus a total row — enough to see the trend at a glance.
func printUsageByDay(entries []usageEntry) {
	byDay := map[string]*usageAgg{}
	var totalIn, totalOut, totalReqs int
	for _, e := range entries {
		bump(byDay, e.Time.Local().Format("2006-01-02"), e)
		totalIn += e.Input
		totalOut += e.Output
		totalReqs++
	}
	days := make([]string, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days)

	fmt.Printf("%-12s  %12s  %12s  %8s\n", "date", "in", "out", "req")
	fmt.Println(strings.Repeat("-", 48))
	for _, d := range days {
		a := byDay[d]
		fmt.Printf("%-12s  %12s  %12s  %8d\n", d, commas(a.in), commas(a.out), a.reqs)
	}
	fmt.Println(strings.Repeat("-", 48))
	fmt.Printf("%-12s  %12s  %12s  %8d\n", "total", commas(totalIn), commas(totalOut), totalReqs)
}

// printUsageDetail is the --detail view: the account/model/project/session
// breakdown, same shape `am usage` always printed before this flag existed.
func printUsageDetail(entries []usageEntry) {
	byAccount := map[string]*usageAgg{}
	byModel := map[string]*usageAgg{}
	byProject := map[string]*usageAgg{}
	bySession := map[string]*usageAgg{}
	lastSeen := map[string]time.Time{}
	var totalIn, totalOut, totalReqs int
	for _, e := range entries {
		bump(byAccount, e.Account, e)
		bump(byModel, orDash(e.Model), e)
		bump(byProject, projectLabel(e.Project), e)
		sid := sessionLabel(e.Session)
		bump(bySession, sid, e)
		if e.Time.After(lastSeen[sid]) {
			lastSeen[sid] = e.Time
		}
		totalIn += e.Input
		totalOut += e.Output
		totalReqs++
	}

	fmt.Println("by account:")
	printUsageTable(byAccount)
	fmt.Println()
	fmt.Println("by model:")
	printUsageTable(byModel)
	fmt.Println()
	fmt.Println("by project:")
	printUsageTable(byProject)
	fmt.Println()
	fmt.Println("by session (most recent first; a new one starts on /clear):")
	printUsageTableByTime(bySession, lastSeen)

	fmt.Println()
	fmt.Println(strings.Repeat("-", 66))
	fmt.Printf("%-30s  in %9s   out %9s   %5d req\n", "total", commas(totalIn), commas(totalOut), totalReqs)
}

// projectLabel shows the project dir's basename (e.g. "ai-cli-accounts"
// instead of "/Users/x/Documents/apps/ai-cli-accounts") — plenty to tell
// projects apart in practice, and lsof couldn't resolve one at all shows as
// "-" (older log entries from before project tracking, or lookup failure).
func projectLabel(dir string) string {
	if dir == "" {
		return "-"
	}
	return filepath.Base(dir)
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

// sessionLabel shortens the session UUID to 8 chars — enough to tell rows
// apart at a glance, still greppable back to the full id in usage.log.
func sessionLabel(id string) string {
	if id == "" {
		return "-"
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// printUsageTableByTime prints rows most-recently-active first, since a
// session's ordinal position (unlike an account or model name) isn't
// meaningful — what matters is which one you were just in.
func printUsageTableByTime(m map[string]*usageAgg, lastSeen map[string]time.Time) {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return lastSeen[names[i]].After(lastSeen[names[j]]) })
	for _, n := range names {
		a := m[n]
		fmt.Printf("  %-28s  in %9s   out %9s   %5d req   last %s\n",
			n, commas(a.in), commas(a.out), a.reqs, lastSeen[n].Local().Format("01-02 15:04"))
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
