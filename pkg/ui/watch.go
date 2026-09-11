package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"amux-accounts/pkg/hook"
	"amux-accounts/pkg/monitor"
	"amux-accounts/pkg/proxy"
	"amux-accounts/pkg/term"
	"amux-accounts/pkg/types"
	"amux-accounts/pkg/usage"
	"golang.org/x/sys/unix"
	goterm "golang.org/x/term"
)

type watchTab int

const (
	tabDash watchTab = iota
	tabAccounts
	tabLogs
	tabUsage
	tabRequests
	tabCount
)

var tabNames = []string{"Dash", "Accounts", "Logs", "Usage", "Requests"}

type usagePeriod int

const (
	periodDay usagePeriod = iota
	periodWeek
	periodMonth
	periodAll
)

var periodLabels = []string{"day", "week", "month", "all"}

type tokAgg struct{ in, out, n int }

type aggRow struct {
	name string
	a    *tokAgg
}

// CmdWatch runs the interactive multi-tab monitoring dashboard.
// Dash = overview KPIs; other tabs = detail views.
func CmdWatch() {
	monitor.EnableTermSink()
	fd := int(os.Stdin.Fd())
	if !goterm.IsTerminal(fd) {
		term.Warn("amux watch needs an interactive terminal")
		return
	}
	old, err := goterm.MakeRaw(fd)
	if err != nil {
		term.Error("raw mode: %v", err)
		return
	}
	defer goterm.Restore(fd, old)
	defer fmt.Print("\x1b[?25h\x1b[0m")
	fmt.Print("\x1b[?25l")

	tab := tabDash
	filter := ""
	editMode := "" // "", "filter", "project"
	editBuf := ""
	scroll := 0
	uPeriod := periodWeek
	uProject := ""

	draw := func() {
		fmt.Print("\x1b[H\x1b[2J")
		fShow, pShow := filter, uProject
		if editMode == "filter" {
			fShow = editBuf + "|"
		}
		if editMode == "project" {
			pShow = editBuf + "|"
		}
		renderWatchChrome(tab, fShow, editMode == "filter", uPeriod, pShow, editMode == "project")
		switch tab {
		case tabDash:
			renderWatchDash()
		case tabAccounts:
			renderWatchAccounts()
		case tabLogs:
			renderWatchLogs(filter, scroll)
		case tabUsage:
			renderWatchUsage(uPeriod, uProject, filter, scroll)
		case tabRequests:
			renderWatchRequests(filter, scroll)
		}
		renderWatchFooter(tab, editMode != "")
	}

	draw()
	buf := make([]byte, 8)
	for {
		n, err := unix.Read(fd, buf)
		if err != nil || n == 0 {
			break
		}
		if editMode != "" {
			switch {
			case buf[0] == 27 && n == 1, buf[0] == 3:
				editMode = ""
			case buf[0] == '\r', buf[0] == '\n':
				if editMode == "filter" {
					filter = editBuf
				} else if editMode == "project" {
					uProject = strings.TrimSpace(editBuf)
				}
				editMode = ""
				scroll = 0
			case buf[0] == 127, buf[0] == 8:
				if editBuf != "" {
					r := []rune(editBuf)
					editBuf = string(r[:len(r)-1])
				}
			case buf[0] >= 32 && buf[0] < 127:
				editBuf += string(buf[0])
			}
			draw()
			continue
		}

		switch {
		case buf[0] == 'q', buf[0] == 3:
			fmt.Print("\x1b[H\x1b[2J")
			return
		case buf[0] == 'r':
			draw()
		case buf[0] == '/' && tab != tabDash && tab != tabAccounts:
			editMode = "filter"
			editBuf = filter
			draw()
		case buf[0] == 'p' && tab == tabUsage:
			editMode = "project"
			editBuf = uProject
			draw()
		case buf[0] == 'c':
			filter = ""
			if tab == tabUsage {
				uProject = ""
			}
			scroll = 0
			draw()
		case buf[0] == 'd' && tab == tabUsage:
			uPeriod = periodDay
			scroll = 0
			draw()
		case buf[0] == 'w' && tab == tabUsage:
			uPeriod = periodWeek
			scroll = 0
			draw()
		case buf[0] == 'm' && tab == tabUsage:
			uPeriod = periodMonth
			scroll = 0
			draw()
		case buf[0] == 'a' && tab == tabUsage:
			uPeriod = periodAll
			scroll = 0
			draw()
		case buf[0] == '\t':
			tab = (tab + 1) % tabCount
			scroll = 0
			draw()
		case buf[0] >= '1' && buf[0] <= '5':
			tab = watchTab(buf[0] - '1')
			scroll = 0
			draw()
		case n == 3 && buf[0] == 27 && buf[1] == '[' && buf[2] == 'A':
			if scroll > 0 {
				scroll--
			}
			draw()
		case n == 3 && buf[0] == 27 && buf[1] == '[' && buf[2] == 'B':
			scroll++
			draw()
		case n == 3 && buf[0] == 27 && buf[1] == '[' && buf[2] == 'C':
			tab = (tab + 1) % tabCount
			scroll = 0
			draw()
		case n == 3 && buf[0] == 27 && buf[1] == '[' && buf[2] == 'D':
			tab = (tab + tabCount - 1) % tabCount
			scroll = 0
			draw()
		}
	}
}

func renderWatchChrome(tab watchTab, filter string, filterEdit bool, period usagePeriod, project string, projectEdit bool) {
	// Header bar: brand left · tabs right (matches amux_dashboard.py)
	left := term.Cyan(term.Bold("amux")) + term.Dim("  ·  gateway · rotate · pool · watch")
	var tabs []string
	for i, name := range tabNames {
		label := fmt.Sprintf("%d:%s", i+1, name)
		if watchTab(i) == tab {
			tabs = append(tabs, term.Badge("info", label))
		} else {
			tabs = append(tabs, term.Dim(label))
		}
	}
	right := strings.Join(tabs, "  ")
	tw := term.TermWidth()
	gap := tw - 2 - term.VisibleLen(left) - term.VisibleLen(right)
	if gap < 2 {
		fmt.Println("  " + left)
		fmt.Println("  " + right)
	} else {
		fmt.Println("  " + left + strings.Repeat(" ", gap) + right)
	}

	if tab == tabDash {
		return
	}
	if tab == tabAccounts {
		fmt.Printf("  %s\n", term.Dim("accounts + pool detail"))
		return
	}
	fDisp := filter
	if filter == "" && !filterEdit {
		fDisp = term.Dim("(all)")
	} else if filterEdit {
		fDisp = term.Cyan(filter)
	} else {
		fDisp = term.Yellow(filter)
	}
	fmt.Printf("  %s %s", term.Dim("filter"), fDisp)
	if tab == tabUsage {
		fmt.Print("  ·  ")
		for i, lab := range periodLabels {
			if usagePeriod(i) == period {
				fmt.Print(term.Badge("ok", lab) + " ")
			} else {
				fmt.Print(term.Dim(lab) + " ")
			}
		}
		pDisp := project
		if project == "" && !projectEdit {
			pDisp = term.Dim("all")
		} else if projectEdit {
			pDisp = term.Cyan(project)
		} else {
			pDisp = term.Yellow(project)
		}
		fmt.Printf(" · %s %s", term.Dim("project"), pDisp)
	}
	fmt.Println()
}

func renderWatchFooter(tab watchTab, editing bool) {
	fmt.Println()
	if editing {
		fmt.Println(term.Dim("  edit · Enter apply · Esc cancel"))
		return
	}
	msg := "2 detail · 3 logs · 4 usage · 5 requests · 1-5 · Tab · r refresh · q quit"
	switch tab {
	case tabAccounts:
		msg = "detail · 1-5 · Tab · r refresh · q quit"
	case tabLogs, tabRequests:
		msg = "↑/↓ · / filter · c clear · 1-5 · Tab · r refresh · q quit"
	case tabUsage:
		msg = "d/w/m/a · p project · / filter · c clear · 1-5 · Tab · r refresh · q quit"
	}
	tw := term.TermWidth()
	if tw > 20 {
		bar := term.BuildPanel("keys", []string{term.Dim(msg)}, tw-2)
		term.PrintLines(bar, "  ")
	} else {
		fmt.Println(term.Dim("  " + msg))
	}
}

// renderWatchDash — 3-column grid like amux_dashboard.py / demo screenshot.
func renderWatchDash() {
	tw := term.TermWidth()
	colW := term.GridColWidth(tw, 3, 1, 2)
	// Prefer stacked single-column on narrow terminals.
	wide := tw >= 100

	proxyBody, accountsBody, poolBody, usageBody, activityBody, requestsBody := dashPanelBodies()

	pProxy := term.BuildPanel("proxy", proxyBody, colW)
	pAccounts := term.BuildPanel("accounts", accountsBody, colW)
	pPool := term.BuildPanel("pool", poolBody, colW)
	pUsage := term.BuildPanel("usage 7d", usageBody, colW)
	pActivity := term.BuildPanel("activity", activityBody, colW)
	pRequests := term.BuildPanel("requests", requestsBody, colW)

	fmt.Println()
	if !wide {
		term.PrintLines(pProxy, "  ")
		term.PrintLines(pAccounts, "  ")
		term.PrintLines(pPool, "  ")
		term.PrintLines(pUsage, "  ")
		term.PrintLines(pActivity, "  ")
		term.PrintLines(pRequests, "  ")
		return
	}

	// Top row: proxy | pool | activity
	// Bottom: accounts | usage | requests  (accounts taller via extra blank lines)
	for len(accountsBody) < len(proxyBody)+4 {
		accountsBody = append(accountsBody, "")
		pAccounts = term.BuildPanel("accounts", accountsBody, colW)
	}

	top := term.JoinColumns(1, pProxy, pPool, pActivity)
	bot := term.JoinColumns(1, pAccounts, pUsage, pRequests)
	term.PrintLines(top, "  ")
	term.PrintLines(bot, "  ")
}

func dashPanelBodies() (proxyB, accountsB, poolB, usageB, activityB, requestsB []string) {
	up := proxy.ProxyUp()
	if !up {
		proxyB = []string{
			term.KVLine("status", term.Badge("off", "down")+"  "+term.Dim("amux proxy up")),
			term.KVLine("update", autoUpdateValue()),
		}
	} else if s, err := fetchProxyStatus(); err != nil {
		proxyB = []string{term.KVLine("status", term.Red(err.Error()))}
	} else {
		mode := s.Mode
		if mode == "" {
			mode = "claude"
		}
		proxyB = []string{
			term.KVLine("status", term.Green(term.Bold("UP"))+"   "+proxy.ProxyAddr()),
			term.KVLine("mode", mode),
			term.KVLine("traffic", fmt.Sprintf("%d tabs  %d switches", s.Sessions, s.Switches)),
			term.KVLine("update", autoUpdateValue()),
		}
		if proxy.IsPublic() {
			proxyB = append(proxyB,
				term.KVLine("bind", term.Yellow("0.0.0.0")+"  "+term.Badge("info", "pub")),
				term.KVLine("public", term.Cyan(proxy.FormatPublicHosts())),
			)
		}

		live, off, cool, dead, total := accountSummaryCounts(s)
		accountsB = []string{
			term.Dim(fmt.Sprintf("summary   %s live/%d   %s off   %s",
				term.Green(fmt.Sprintf("%d", live)), total,
				term.Dim(fmt.Sprintf("%d", off)),
				dashExtra(cool, dead),
			)),
		}
		shown := 0
		for _, a := range s.Accounts {
			if shown >= 6 {
				accountsB = append(accountsB, term.Dim(fmt.Sprintf("... %d more", len(s.Accounts)-shown)))
				break
			}
			name := a.Account
			if name == "" {
				name = a.Profile
			}
			flag := "IDLE"
			flagStyle := term.Dim
			if a.Dead {
				flag, flagStyle = "DEAD", term.Red
			} else if a.Disabled {
				flag, flagStyle = "OFF", term.Dim
			} else if a.Active && s.Mode != "provider" {
				flag, flagStyle = "LIVE", term.Green
			} else if a.Active {
				flag, flagStyle = "PIN", term.Yellow
			}
			pct := 0.0
			age := "7d"
			if a.SevenDUsed != nil {
				pct = *a.SevenDUsed * 100
			} else if a.FiveHUsed != nil {
				pct = *a.FiveHUsed * 100
				age = "5h"
			}
			accountsB = append(accountsB, fmt.Sprintf("%s %-26s %s %s %3.0f%%",
				flagStyle(fmt.Sprintf("%-4s", flag)),
				shortName(name, 26),
				term.Dim(age),
				term.ProgressBar(pct/100, 10),
				pct,
			))
			shown++
		}

		pin, _, pCool, pIdle, pTotal := poolSummaryCounts(s)
		poolB = []string{
			term.KVLine("providers", term.Bold(fmt.Sprintf("%d", pTotal))),
			term.KVLine("state", fmt.Sprintf("%s  %s idle  %s",
				term.Yellow(fmt.Sprintf("%d pin", pin)),
				fmt.Sprintf("%d", pIdle),
				term.Red(fmt.Sprintf("%d cool", pCool)),
			)),
		}
	}
	if accountsB == nil {
		accountsB = []string{term.Dim("(no accounts)")}
	}
	if poolB == nil {
		poolB = []string{term.Dim("(proxy down)")}
	}

	since := time.Now().AddDate(0, 0, -7)
	entries := usage.LoadUsageEntries(since)
	var in, out int
	for _, e := range entries {
		in += e.Input
		out += e.Output
	}
	ev := monitor.LoadEvents(80, "")
	errN := 0
	for _, e := range ev {
		t := strings.ToUpper(e.Tag)
		if t == "ERROR" || t == "AUTH" || t == "FAIL OVER" || t == "FAILOVER" {
			errN++
		}
	}
	usageB = []string{
		term.KVLine("requests", term.Bold(usage.Commas(len(entries)))),
		term.KVLine("tokens", fmt.Sprintf("in %s   out %s",
			usage.Commas(in), usage.Commas(out))),
		term.KVLine("", fmt.Sprintf("total %s", term.Bold(usage.Commas(in+out)))),
		term.KVLine("alerts", fmt.Sprintf("%d  / %d events", errN, len(ev))),
	}

	if len(ev) == 0 {
		activityB = []string{term.Dim("—")}
	} else {
		start := len(ev) - 8
		if start < 0 {
			start = 0
		}
		for i := start; i < len(ev); i++ {
			e := ev[i]
			activityB = append(activityB, fmt.Sprintf("%s  %s %s",
				term.Dim(e.Time.Local().Format("15:04:05")),
				term.Magenta(fmt.Sprintf("%-6s", e.Tag)),
				truncate(e.Message, 28),
			))
		}
	}

	reqs := monitor.LoadRequests(40, "")
	if len(reqs) == 0 {
		requestsB = []string{term.Dim("—")}
	} else {
		start := len(reqs) - 8
		if start < 0 {
			start = 0
		}
		for i := start; i < len(reqs); i++ {
			e := reqs[i]
			acct := e.Account
			if acct == "" {
				acct = "-"
			}
			st := term.Green("ok ")
			if e.Error != "" {
				st = term.Red("err")
			}
			requestsB = append(requestsB, fmt.Sprintf("%s  %s %-5s %-14s %s",
				term.Dim(e.Time.Local().Format("15:04:05")),
				st,
				nz(e.Dialect, "?"),
				term.Cyan(shortName(acct, 14)),
				term.Dim(fmt.Sprintf("%dms", e.DurationMs)),
			))
		}
	}
	return
}

func autoUpdateValue() string {
	if hook.IsAutoUpdateEnabled() {
		return term.Green("on")
	}
	return term.Dim("off")
}

func renderWatchAccounts() { printStatusBody() }

func renderWatchLogs(filter string, scroll int) {
	fmt.Println(term.Bold("  Logs") + term.Dim("  · rotate · failover · auth"))
	fmt.Println()
	ev := monitor.LoadEvents(200, filter)
	if len(ev) == 0 {
		term.Info("No events yet. Start proxy / chat — rotate & failover appear here.")
		return
	}
	view := windowSlice(len(ev), scroll, 16)
	for i := view.from; i < view.to; i++ {
		e := ev[i]
		fmt.Printf("  %s  %s  %s\n",
			term.Dim(e.Time.Local().Format("15:04:05")),
			colorTag(e.Tag),
			e.Message,
		)
	}
	fmt.Printf("\n  %s\n", term.Dim(fmt.Sprintf("showing %d-%d of %d", view.from+1, view.to, len(ev))))
}

func renderWatchUsage(period usagePeriod, project, textFilter string, scroll int) {
	since := usageSince(period)
	entries := usage.LoadUsageEntries(since)
	textFilter = strings.ToLower(strings.TrimSpace(textFilter))
	project = strings.TrimSpace(project)

	var filtered []types.UsageEntry
	for _, e := range entries {
		if !since.IsZero() && e.Time.Before(since) {
			continue
		}
		if project != "" && !strings.EqualFold(usage.ProjectLabel(e.Project), project) {
			continue
		}
		if textFilter != "" {
			blob := strings.ToLower(e.Account + " " + e.Model + " " + e.Project + " " + e.Session)
			if !strings.Contains(blob, textFilter) {
				continue
			}
		}
		filtered = append(filtered, e)
	}

	fmt.Printf("  %s  %s", term.Bold("Usage · "+periodLabels[period]), term.Dim(fmt.Sprintf("%d requests", len(filtered))))
	if project != "" {
		fmt.Printf("  %s", term.Yellow("project="+project))
	}
	fmt.Print("\n\n")

	if len(filtered) == 0 {
		term.Info("No usage for this window. Try d/w/m/a or clear project (c / p).")
		return
	}

	byAcct := map[string]*tokAgg{}
	byProj := map[string]*tokAgg{}
	var totalIn, totalOut int
	for _, e := range filtered {
		a := e.Account
		if a == "" {
			a = "(unknown)"
		}
		if byAcct[a] == nil {
			byAcct[a] = &tokAgg{}
		}
		byAcct[a].in += e.Input
		byAcct[a].out += e.Output
		byAcct[a].n++
		pl := usage.ProjectLabel(e.Project)
		if byProj[pl] == nil {
			byProj[pl] = &tokAgg{}
		}
		byProj[pl].in += e.Input
		byProj[pl].out += e.Output
		byProj[pl].n++
		totalIn += e.Input
		totalOut += e.Output
	}

	fmt.Printf("  %s  in %s  out %s  total %s\n\n",
		term.Badge("ok", "sum"),
		term.Cyan(usage.Commas(totalIn)),
		term.Green(usage.Commas(totalOut)),
		term.Bold(usage.Commas(totalIn+totalOut)),
	)

	fmt.Println(term.Cyan(term.Bold("  By account")))
	rows := sortAgg(byAcct)
	view := windowSlice(len(rows), scroll, 8)
	for i := view.from; i < view.to; i++ {
		r := rows[i]
		tot := r.a.in + r.a.out
		fmt.Printf("  %s  %s\n", term.Bold(r.name), term.Dim(fmt.Sprintf("%d req", r.a.n)))
		fmt.Printf("      in %-10s out %-10s %s\n",
			usage.Commas(r.a.in), usage.Commas(r.a.out),
			term.ProgressBar(float64(tot)/float64(max(totalIn+totalOut, 1)), 16))
	}

	fmt.Println()
	fmt.Println(term.Cyan(term.Bold("  By project")))
	prows := sortAgg(byProj)
	for i, r := range prows {
		if i >= 6 {
			fmt.Printf("  %s\n", term.Dim(fmt.Sprintf("... %d more — filter with p", len(prows)-6)))
			break
		}
		fmt.Printf("  %s  %s  in %s  out %s\n",
			term.Bold(r.name),
			term.Dim(fmt.Sprintf("%d req", r.a.n)),
			term.Cyan(usage.Commas(r.a.in)),
			term.Green(usage.Commas(r.a.out)),
		)
	}
}

func sortAgg(m map[string]*tokAgg) []aggRow {
	var rows []aggRow
	for k, v := range m {
		rows = append(rows, aggRow{k, v})
	}
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].a.in+rows[j].a.out > rows[i].a.in+rows[i].a.out {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	return rows
}

func usageSince(p usagePeriod) time.Time {
	now := time.Now()
	switch p {
	case periodDay:
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	case periodWeek:
		return now.AddDate(0, 0, -7)
	case periodMonth:
		return now.AddDate(0, -1, 0)
	default:
		return time.Time{}
	}
}

func renderWatchRequests(filter string, scroll int) {
	fmt.Println(term.Bold("  Requests") + term.Dim("  · chat I/O through proxy/bridge"))
	fmt.Println()
	reqs := monitor.LoadRequests(100, filter)
	if len(reqs) == 0 {
		term.Info("No chat requests logged yet. Traffic through proxy/bridge appears here.")
		return
	}
	view := windowSlice(len(reqs), scroll, 8)
	for i := view.from; i < view.to; i++ {
		e := reqs[i]
		dialect := e.Dialect
		if dialect == "" {
			dialect = "?"
		}
		acct := e.Account
		if acct == "" {
			acct = "-"
		}
		fmt.Printf("  %s  %s  %s  %s  %s\n",
			term.Dim(e.Time.Local().Format("15:04:05")),
			term.Cyan(dialect),
			term.Bold(acct),
			term.Dim(e.Model),
			term.Dim(fmt.Sprintf("%dms", e.DurationMs)),
		)
		if e.Error != "" {
			fmt.Printf("      %s %s\n", term.Red("err"), e.Error)
		} else {
			fmt.Printf("      %s %s\n", term.Yellow("in "), term.Dim(oneLine(e.Input)))
			fmt.Printf("      %s %s\n", term.Green("out"), term.Dim(oneLine(e.Output)))
		}
		if e.InTokens+e.OutTokens > 0 || e.StopReason != "" {
			fmt.Printf("      %s\n", term.Dim(fmt.Sprintf("tokens in=%d out=%d  stop=%s", e.InTokens, e.OutTokens, e.StopReason)))
		}
		fmt.Println()
	}
	fmt.Printf("  %s\n", term.Dim(fmt.Sprintf("showing %d-%d of %d", view.from+1, view.to, len(reqs))))
}

func colorTag(tag string) string {
	switch strings.ToUpper(strings.TrimSpace(tag)) {
	case "ROTATE", "SWITCH":
		return term.Magenta(fmt.Sprintf("%-9s", tag))
	case "FAIL OVER", "FAILOVER", "POOL":
		return term.Cyan(fmt.Sprintf("%-9s", tag))
	case "ERROR", "AUTH":
		return term.Red(fmt.Sprintf("%-9s", tag))
	case "WARN", "DEGRADED":
		return term.Yellow(fmt.Sprintf("%-9s", tag))
	case "OK":
		return term.Green(fmt.Sprintf("%-9s", tag))
	default:
		return term.Dim(fmt.Sprintf("%-9s", tag))
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	return monitor.TruncateRunes(s, 100)
}

func truncate(s string, n int) string {
	return monitor.TruncateRunes(s, n)
}

func shortName(s string, n int) string {
	return monitor.TruncateRunes(s, n)
}

func nz(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func dashExtra(cool, dead int) string {
	parts := []string{}
	if cool > 0 {
		parts = append(parts, term.Yellow(fmt.Sprintf("%d cool", cool)))
	}
	if dead > 0 {
		parts = append(parts, term.Red(fmt.Sprintf("%d dead", dead)))
	}
	if len(parts) == 0 {
		return term.Dim("ok")
	}
	return strings.Join(parts, " · ")
}

type viewRange struct{ from, to int }

func windowSlice(n, scroll, page int) viewRange {
	if n == 0 {
		return viewRange{0, 0}
	}
	if page <= 0 {
		page = 10
	}
	maxScroll := n - page
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	if scroll < 0 {
		scroll = 0
	}
	to := n - scroll
	from := to - page
	if from < 0 {
		from = 0
	}
	return viewRange{from, to}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
