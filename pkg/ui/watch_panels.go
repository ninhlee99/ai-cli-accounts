package ui

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"amux-accounts/pkg/hook"
	"amux-accounts/pkg/monitor"
	"amux-accounts/pkg/provider"
	"amux-accounts/pkg/proxy"
	"amux-accounts/pkg/types"
	"amux-accounts/pkg/usage"
	"github.com/charmbracelet/lipgloss"
)

type watchAccountRow struct {
	Group    string // CLAUDE CODE, CLAUDE WEB, CODEX, …
	ID       string
	Name     string
	Flag     string // LIVE/PIN/IDLE/OFF/DEAD/POOL/OUT
	InPool   bool
	FiveH    *float64
	FiveHRst string
	SevenD   *float64
	SevenDRst string
	Model    string
	Extra    string
}

type watchSnap struct {
	ProxyUp     bool
	ProxyAddr   string
	Mode        string
	ActiveAcct  string // who is serving now (claude profile or provider id)
	Sessions    int
	Switches    int
	Public      bool
	PublicHosts string
	AutoUpdate  bool

	Accounts []watchAccountRow
	Live, Off, Cool, Dead, AcctTotal int
	PoolIn, PoolOut                  int

	Providers, PoolPin, PoolIdle, PoolCool int
	Status                                 *proxyStatus

	UsageRequests, TokensIn, TokensOut int
	Alerts, AlertTotal                 int
	History                            []int
	UsageByAccount                     []aggRow
	UsageByProject                     []aggRow
	UsageEntries                       []types.UsageEntry

	Activity []types.EventEntry
	Requests []types.RequestEntry
}

var accountGroupOrder = []string{
	"CLAUDE CODE",
	"CLAUDE WEB",
	"CODEX",
	"CHATGPT",
	"GEMINI WEB",
	"GEMINI API",
	"ANTIGRAVITY",
	"CURSOR",
	"GROQ API",
	"OPENROUTER API",
	"OPENAI API",
	"API",
}

// accountGroupFor maps provider type/id/url → watch Accounts panel title.
//
// Naming scheme:
//   Product / client sessions  → "CLAUDE CODE", "CODEX", "CURSOR", "ANTIGRAVITY"
//   Web sessions               → "CLAUDE WEB", "GEMINI WEB", "CHATGPT"
//   HTTP API keys              → "<PROVIDER> API" (OPENROUTER API, GEMINI API, …)
//
// Identity IDs (row Name prefers these):
//   claude:web:<local> | gemini:web:<local> | chatgpt:<local>
//   gemini:api:<n> | codex:<profile> | cursor:<id> | antigravity:<id>
func accountGroupFor(typ, id, baseURL string) string {
	idL := strings.ToLower(id)
	typL := strings.ToLower(typ)
	host := ""
	if u, err := url.Parse(baseURL); err == nil {
		host = strings.ToLower(u.Host)
	}
	switch {
	case typL == "claude_web" || strings.HasPrefix(idL, "claudeweb") || strings.HasPrefix(idL, "claude:web"):
		return "CLAUDE WEB"
	case typL == "chatgpt_web" || strings.HasPrefix(idL, "chatgptweb") || strings.HasPrefix(idL, "chatgpt:") || strings.HasPrefix(idL, "chatgpt"):
		return "CHATGPT"
	case typL == "codex_cli" || strings.HasPrefix(idL, "codex"):
		return "CODEX"
	case typL == "gemini_web" || strings.HasPrefix(idL, "geminiweb") || strings.HasPrefix(idL, "gemini:web"):
		return "GEMINI WEB"
	case typL == "gemini" || strings.HasPrefix(idL, "geminiapi") || strings.HasPrefix(idL, "gemini:api") || strings.Contains(host, "generativelanguage") || strings.Contains(host, "aistudio"):
		return "GEMINI API"
	case strings.Contains(idL, "antigravity") || strings.Contains(idL, "agy"):
		return "ANTIGRAVITY"
	case strings.Contains(idL, "cursor"):
		return "CURSOR"
	case strings.HasPrefix(idL, "groq") || strings.Contains(host, "groq") || strings.Contains(idL, "grok"):
		return "GROQ API"
	case strings.Contains(host, "openrouter") || strings.Contains(idL, "openrouter"):
		return "OPENROUTER API"
	case strings.Contains(host, "api.openai.com") || strings.HasPrefix(idL, "openai"):
		return "OPENAI API"
	case typL == "openai_compatible":
		return "API"
	default:
		return "API"
	}
}

// accountPrimaryLabel is what Accounts tab shows after POOL/LIVE/OUT.
// Prefer stable identity IDs (brand:…); fall back to email/name.
func accountPrimaryLabel(a watchAccountRow) string {
	id := types.DisplayAccountID(strings.TrimSpace(a.ID))
	name := strings.TrimSpace(a.Name)
	switch {
	case isIdentityID(id):
		return id
	case name != "" && !isGenericPoolLabel(name):
		return name
	case id != "":
		return id
	default:
		return name
	}
}

func isIdentityID(id string) bool {
	if id == "" || strings.Contains(id, " ") {
		return false
	}
	// brand:handle or brand:method:handle
	n := strings.Count(id, ":")
	return n >= 1 && n <= 2
}

func isGenericPoolLabel(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "codex cli", "codex", "cursor", "antigravity", "pool":
		return true
	default:
		return false
	}
}

func loadWatchSnap(period usagePeriod, project, filter string) watchSnap {
	s := watchSnap{
		ProxyUp:    proxy.ProxyUp(),
		ProxyAddr:  proxy.ProxyAddr(),
		AutoUpdate: hook.IsAutoUpdateEnabled(),
		Public:     proxy.IsPublic(),
	}
	if s.Public {
		s.PublicHosts = proxy.FormatPublicHosts()
	}

	textFilter := strings.ToLower(strings.TrimSpace(filter))

	if s.ProxyUp {
		if st, err := fetchProxyStatus(); err == nil && st != nil {
			s.Status = st
			s.Mode = st.Mode
			if s.Mode == "" {
				s.Mode = "claude"
			}
			s.ActiveAcct = resolveActiveAccount(st)
			s.Sessions = st.Sessions
			s.Switches = st.Switches
			s.Live, s.Off, s.Cool, s.Dead, s.AcctTotal = accountSummaryCounts(st)
			s.PoolPin, _, s.PoolCool, s.PoolIdle, s.Providers = poolSummaryCounts(st)

			for _, a := range st.Accounts {
				name := a.Account
				if name == "" {
					name = a.Profile
				}
				flag := "IDLE"
				inPool := !a.Disabled && !a.Dead
				if a.Dead {
					flag = "DEAD"
					inPool = false
				} else if a.Disabled {
					flag = "OUT"
					inPool = false
				} else if a.Active && st.Mode != "provider" {
					flag = "LIVE"
				} else if a.Active {
					flag = "PIN"
				} else {
					flag = "POOL"
				}
				row := watchAccountRow{
					Group:     "CLAUDE CODE",
					ID:        a.Profile,
					Name:      name,
					Flag:      flag,
					InPool:    inPool,
					FiveH:     a.FiveHUsed,
					FiveHRst:  a.FiveHReset,
					SevenD:    a.SevenDUsed,
					SevenDRst: a.SevenDReset,
				}
				if textFilter == "" || matchAccountFilter(row, textFilter) {
					s.Accounts = append(s.Accounts, row)
				}
				if inPool {
					s.PoolIn++
				} else {
					s.PoolOut++
				}
			}

			poolIDs := map[string]bool{}
			for _, p := range st.Pool {
				if id, _ := p["id"].(string); id != "" {
					poolIDs[id] = true
				}
			}
			_ = poolIDs
		}
	}

	if f, err := provider.LoadConfigFile(provider.DefaultAccountsPath()); err == nil && f != nil {
		for _, p := range f.Providers {
			if !p.HasCredentials() && p.Type != "codex_cli" {
				continue
			}
			inPool := p.InRotatePool()
			flag := "POOL"
			if !inPool {
				flag = "OUT"
			}
			name := p.Account
			if name == "" {
				name = p.ID
			}
			// Prefer identity ID as the human label when available.
			if isIdentityID(p.ID) {
				name = p.ID
			}
			row := watchAccountRow{
				Group:  accountGroupFor(p.Type, p.ID, p.BaseURL),
				ID:     p.ID,
				Name:   name,
				Flag:   flag,
				InPool: inPool,
				Model:  p.Model,
				Extra:  p.Type,
			}
			if textFilter == "" || matchAccountFilter(row, textFilter) {
				s.Accounts = append(s.Accounts, row)
			}
			if inPool {
				s.PoolIn++
			} else {
				s.PoolOut++
			}
		}
		if row, ok := provider.CodexAutoRow(f.Providers); ok {
			r := watchAccountRow{
				Group:  "CODEX",
				ID:     row.ID,
				Name:   row.ID, // e.g. codexcli:01 / codex:<profile> — same style as Claude Code rows
				Flag:   "POOL",
				InPool: true,
				Extra:  "codex_cli",
			}
			dup := false
			for _, a := range s.Accounts {
				if a.ID == r.ID {
					dup = true
					break
				}
			}
			if !dup && (textFilter == "" || matchAccountFilter(r, textFilter)) {
				s.Accounts = append(s.Accounts, r)
				s.PoolIn++
			}
		}
	} else if row, ok := provider.CodexAutoRow(nil); ok {
		r := watchAccountRow{Group: "CODEX", ID: row.ID, Name: row.ID, Flag: "POOL", InPool: true, Extra: "codex_cli"}
		if textFilter == "" || matchAccountFilter(r, textFilter) {
			s.Accounts = append(s.Accounts, r)
			s.PoolIn++
		}
	}

	// Always load ≤30d so day [] navigation works across periods.
	raw := usage.LoadUsageEntries(usageSince(periodMonth))
	periodStart := usageSince(period)
	project = strings.TrimSpace(project)
	var in, out, n int
	histBuckets := make([]int, 24)
	now := time.Now()
	byAcct := map[string]*tokAgg{}
	byProj := map[string]*tokAgg{}
	var kept []types.UsageEntry
	for _, e := range raw {
		if project != "" && !strings.EqualFold(usage.ProjectLabel(e.Project), project) {
			continue
		}
		if textFilter != "" {
			blob := strings.ToLower(e.Account + " " + e.Model + " " + e.Project + " " + e.Session)
			if !strings.Contains(blob, textFilter) {
				continue
			}
		}
		kept = append(kept, e)
		if e.Time.Before(periodStart) {
			continue
		}
		n++
		in += e.Input
		out += e.Output
		ageH := int(now.Sub(e.Time).Hours())
		if ageH >= 0 && ageH < 24 {
			histBuckets[23-ageH]++
		}
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
		if pl == "" {
			pl = "(none)"
		}
		if byProj[pl] == nil {
			byProj[pl] = &tokAgg{}
		}
		byProj[pl].in += e.Input
		byProj[pl].out += e.Output
		byProj[pl].n++
	}
	s.UsageRequests = n
	s.TokensIn, s.TokensOut = in, out
	s.History = histBuckets
	s.UsageByAccount = sortAgg(byAcct)
	s.UsageByProject = sortAgg(byProj)
	s.UsageEntries = kept

	s.Activity = monitor.LoadEvents(200, filter)
	errN := 0
	for _, e := range s.Activity {
		t := strings.ToUpper(e.Tag)
		if t == "ERROR" || t == "AUTH" || t == "FAIL OVER" || t == "FAILOVER" {
			errN++
		}
	}
	s.Alerts = errN
	s.AlertTotal = len(s.Activity)
	s.Requests = monitor.LoadRequests(100, filter)
	return s
}

func matchAccountFilter(a watchAccountRow, f string) bool {
	blob := strings.ToLower(a.Group + " " + a.ID + " " + a.Name + " " + a.Flag + " " + a.Model + " " + a.Extra)
	return strings.Contains(blob, f)
}

// resolveActiveAccount picks who gateway is serving: Claude LIVE profile, or
// provider-pool last_used / preferred pin.
func resolveActiveAccount(st *proxyStatus) string {
	if st == nil {
		return ""
	}
	if st.Mode == "provider" {
		var preferred, lastUsed string
		for _, p := range st.Pool {
			id, _ := p["id"].(string)
			if id == "" {
				continue
			}
			if preferredFlag, _ := p["preferred"].(bool); preferredFlag {
				preferred = id
			}
			if last, _ := p["last_used"].(bool); last {
				lastUsed = id
			}
		}
		if lastUsed != "" {
			return lastUsed
		}
		return preferred
	}
	for _, a := range st.Accounts {
		if !a.Active {
			continue
		}
		if a.Account != "" {
			return a.Account
		}
		return a.Profile
	}
	return ""
}

func modeLine(s watchSnap) string {
	mode := s.Mode
	if mode == "" {
		mode = "claude"
	}
	switch mode {
	case "provider":
		return valStyle.Render("provider") + rowDim.Render("  (pool)")
	case "claude":
		return valStyle.Render("claude") + rowDim.Render("  (oauth)")
	default:
		return valStyle.Render(mode)
	}
}

func panelProxy(w, h int, s watchSnap) string {
	lines := []string{}
	if !s.ProxyUp {
		lines = append(lines,
			watchKV("status", statusPill(false)+"  "+rowDim.Render("amux proxy up")),
			watchKV("update", autoUpdateLabel(s.AutoUpdate)),
		)
	} else {
		via := rowDim.Render("—")
		if s.ActiveAcct != "" {
			via = valStyle.Render(truncateRunes(s.ActiveAcct, maxInt(w-12, 10)))
		}
		lines = append(lines,
			watchKV("status", statusPill(true)+"  "+valStyle.Render(s.ProxyAddr)),
			watchKV("mode", modeLine(s)),
			watchKV("via", via),
			watchKV("traffic", fmt.Sprintf("%d tabs · %d switches", s.Sessions, s.Switches)),
			watchKV("update", autoUpdateLabel(s.AutoUpdate)),
			watchKV("pool", fmt.Sprintf("%s in · %s out",
				pillUp.Render(fmt.Sprintf("%d", s.PoolIn)),
				rowDim.Render(fmt.Sprintf("%d", s.PoolOut)))),
		)
		if s.Public {
			lines = append(lines,
				watchKV("bind", pillWarn.Render("0.0.0.0")+"  pub"),
				watchKV("public", valStyle.Render(truncateRunes(s.PublicHosts, maxInt(w-14, 12)))),
			)
		}
	}
	return watchPanel("PROXY", w, h, colBlue, strings.Join(lines, "\n"))
}

func autoUpdateLabel(on bool) string {
	if on {
		return pillUp.Render("on")
	}
	return rowDim.Render("off")
}

func panelAccounts(w, h int, s watchSnap) string {
	summary := rowDim.Render(fmt.Sprintf("%d live · %d pool · %d out", s.Live, s.PoolIn, s.PoolOut))
	innerW := maxInt(w-6, 12) // panel border+padding
	rows := []string{summary, ""}
	shown := 0
	for _, a := range s.Accounts {
		if a.Group != "CLAUDE CODE" {
			continue
		}
		if shown >= 6 {
			rows = append(rows, rowDim.Render("… more in Accounts tab"))
			break
		}
		rows = append(rows, accountDashBlock(a, innerW)...)
		rows = append(rows, "")
		shown++
	}
	if shown == 0 {
		rows = append(rows, rowDim.Render("(no claude profiles)"))
	}
	return watchPanel("ACCOUNTS", w, h, colPurple, strings.Join(rows, "\n"))
}

func accountDashBlock(a watchAccountRow, innerW int) []string {
	flagStyle := rowDim
	switch a.Flag {
	case "PIN", "POOL":
		flagStyle = lipgloss.NewStyle().Foreground(colYellow).Bold(true)
	case "LIVE":
		flagStyle = lipgloss.NewStyle().Foreground(colGreen).Bold(true)
	case "DEAD", "OUT", "OFF":
		flagStyle = pillDown
	}
	nameW := maxInt(innerW-10, 8)
	head := fmt.Sprintf("%s %s",
		flagStyle.Render(fmt.Sprintf("%-4s", a.Flag)),
		valStyle.Render(truncateRunes(a.Name, nameW)),
	)
	out := []string{head}
	if !groupShowsLimits(a.Group) {
		return out
	}
	barW := maxInt(innerW-14, 8)
	hasAny := false
	if groupShowsFiveH(a.Group) && a.FiveH != nil {
		hasAny = true
		pct := int(*a.FiveH * 100)
		out = append(out, rowDim.Render("5h  ")+watchBar(pct, barW)+rowDim.Render(fmt.Sprintf(" %3d%%", pct)))
	}
	if a.SevenD != nil {
		hasAny = true
		pct := int(*a.SevenD * 100)
		out = append(out, rowDim.Render("tuần ")+watchBar(pct, barW)+rowDim.Render(fmt.Sprintf(" %3d%%", pct)))
	}
	if !hasAny {
		out = append(out, rowDim.Render("limits pending"))
	}
	return out
}

func accountDashLine(a watchAccountRow, nameWidth int) string {
	// kept for callers; prefer accountDashBlock
	return strings.Join(accountDashBlock(a, nameWidth+20), " ")
}

func panelAccountsGrouped(w, h int, s watchSnap, scroll int) string {
	byGroup := map[string][]watchAccountRow{}
	for _, a := range s.Accounts {
		byGroup[a.Group] = append(byGroup[a.Group], a)
	}
	var groups []string
	for _, g := range accountGroupOrder {
		if len(byGroup[g]) > 0 {
			groups = append(groups, g)
		}
	}
	for g, rows := range byGroup {
		known := false
		for _, k := range accountGroupOrder {
			if k == g {
				known = true
				break
			}
		}
		if !known && len(rows) > 0 {
			groups = append(groups, g)
		}
	}
	if len(groups) == 0 {
		return watchPanel("ACCOUNTS", w, h, colPurple, rowDim.Render("(no accounts)"))
	}

	cols := 1
	switch {
	case w >= 140:
		cols = 3
	case w >= 90:
		cols = 2
	}
	gap := 1
	colW := (w - gap*(cols-1)) / cols
	if colW < 28 {
		colW = 28
		cols = maxInt(1, w/(colW+gap))
		colW = (w - gap*(cols-1)) / cols
	}

	accents := []lipgloss.Color{colPurple, colBlue, colCyan, colGreen, colOrange, colYellow, colRed}
	hint := rowDim.Render(fmt.Sprintf("POOL=rotate · OUT=X-Provider · ↑↓ page · %d groups · %d cols", len(groups), cols))

	// Build one panel per group (auto height from content).
	type box struct {
		title string
		body  string
		accent lipgloss.Color
		lines int
	}
	var boxes []box
	for i, g := range groups {
		rows := byGroup[g]
		var lines []string
		inner := maxInt(colW-6, 16)
		for _, a := range rows {
			lines = append(lines, accountDetailBlock(a, inner)...)
			lines = append(lines, "")
		}
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		boxes = append(boxes, box{
			title:  g,
			body:   strings.Join(lines, "\n"),
			accent: accents[i%len(accents)],
			lines:  len(lines) + 2, // title + body approx
		})
	}

	// Pack into rows of `cols` panels; scroll pages by row.
	var rows [][]box
	for i := 0; i < len(boxes); i += cols {
		end := i + cols
		if end > len(boxes) {
			end = len(boxes)
		}
		rows = append(rows, boxes[i:end])
	}
	if scroll < 0 {
		scroll = 0
	}
	if scroll >= len(rows) {
		scroll = maxInt(len(rows)-1, 0)
	}

	// How many grid rows fit in height (rough: each box ~ min 8 lines).
	avail := maxInt(h-3, 8)
	var outRows []string
	outRows = append(outRows, hint)
	used := 1
	shown := 0
	for r := scroll; r < len(rows); r++ {
		rowBoxes := rows[r]
		maxH := 0
		panels := make([]string, 0, len(rowBoxes))
		for _, b := range rowBoxes {
			bh := maxInt(b.lines+3, 6)
			if bh > maxH {
				maxH = bh
			}
		}
		// Cap row height so more rows can show; content scrolls inside via page.
		if maxH > avail/2 && len(rows)-scroll > 1 {
			maxH = maxInt(avail/2, 8)
		}
		if used+maxH > avail && shown > 0 {
			break
		}
		for _, b := range rowBoxes {
			panels = append(panels, watchPanel(b.title, colW, maxH, b.accent, b.body))
		}
		// Pad incomplete last visual row
		for len(panels) < cols && r == len(rows)-1 {
			break
		}
		outRows = append(outRows, lipgloss.JoinHorizontal(lipgloss.Top, joinWithGap(panels, gap)...))
		used += maxH
		shown++
	}
	pageLine := rowDim.Render(fmt.Sprintf("groups page %d/%d", scroll+1, maxInt(len(rows), 1)))
	main := strings.Join(outRows, "\n")
	// Pin page line to bottom of content area (not floating under short content).
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Width(w).Height(maxInt(h-1, 1)).Align(lipgloss.Left, lipgloss.Top).Render(main),
		pageLine,
	)
}

func joinWithGap(panels []string, gap int) []string {
	if gap <= 0 || len(panels) == 0 {
		return panels
	}
	out := make([]string, 0, len(panels)*2-1)
	sp := strings.Repeat(" ", gap)
	for i, p := range panels {
		if i > 0 {
			out = append(out, sp)
		}
		out = append(out, p)
	}
	return out
}

func accountDetailBlock(a watchAccountRow, width int) []string {
	flagStyle := rowDim
	switch a.Flag {
	case "LIVE", "POOL", "PIN":
		flagStyle = pillUp
		if a.Flag == "PIN" || a.Flag == "POOL" {
			flagStyle = lipgloss.NewStyle().Foreground(colYellow).Bold(true)
		}
	case "OUT", "OFF", "DEAD":
		flagStyle = pillDown
	}
	nameW := maxInt(width-10, 8)
	label := accountPrimaryLabel(a)
	title := fmt.Sprintf("%s %s",
		flagStyle.Render(fmt.Sprintf("%-4s", a.Flag)),
		valStyle.Render(truncateRunes(label, nameW)),
	)
	out := []string{title}
	// Secondary: email when primary is identity id, or raw id when primary is email.
	if a.Name != "" && a.Name != label && strings.Contains(a.Name, "@") {
		out = append(out, rowDim.Render("  "+truncateRunes(a.Name, width-2)))
	} else if a.ID != "" && a.ID != label {
		out = append(out, rowDim.Render("  "+truncateRunes(a.ID, width-2)))
	}
	if a.Model != "" {
		out = append(out, rowDim.Render("  model ")+valStyle.Render(truncateRunes(a.Model, maxInt(width-10, 6))))
	}
	if !groupShowsLimits(a.Group) {
		return out
	}
	barW := maxInt(width-14, 8)
	show5h := groupShowsFiveH(a.Group)
	hasAny := false
	if show5h && a.FiveH != nil {
		hasAny = true
		out = append(out, rowDim.Render("  limit 5h  ")+watchBar(int(*a.FiveH*100), barW))
		out = append(out, rowDim.Render(fmt.Sprintf("             %3.0f%%%s", *a.FiveH*100, resetShort(a.FiveHRst))))
	}
	if a.SevenD != nil {
		hasAny = true
		out = append(out, rowDim.Render("  limit tuần ")+watchBar(int(*a.SevenD*100), barW))
		out = append(out, rowDim.Render(fmt.Sprintf("             %3.0f%%%s", *a.SevenD*100, resetShort(a.SevenDRst))))
	}
	if !hasAny {
		out = append(out, rowDim.Render("  limits pending (need traffic)"))
	}
	return out
}

func groupShowsLimits(group string) bool {
	switch group {
	case "CLAUDE CODE", "CLAUDE WEB", "CODEX", "CURSOR", "ANTIGRAVITY":
		return true
	default:
		return false
	}
}

// Cursor has weekly quota only — no 5h window.
func groupShowsFiveH(group string) bool {
	return groupShowsLimits(group) && group != "CURSOR"
}

func resetShort(iso string) string {
	if iso == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return ""
	}
	return "  reset " + t.Local().Format("15:04")
}

func panelPool(w, h int, s watchSnap) string {
	if !s.ProxyUp {
		return watchPanel("POOL", w, h, colGreen, rowDim.Render("(proxy down)"))
	}
	lines := []string{
		watchKV("providers", valStyle.Render(fmt.Sprintf("%d", s.Providers))),
		watchKV("state", lipgloss.NewStyle().Foreground(colYellow).Render(fmt.Sprintf("%d pin", s.PoolPin))+
			"  "+rowDim.Render(fmt.Sprintf("%d idle", s.PoolIdle))+
			"  "+lipgloss.NewStyle().Foreground(colRed).Render(fmt.Sprintf("%d cool", s.PoolCool))),
		watchKV("rotate", fmt.Sprintf("%d in-pool · %d out", s.PoolIn, s.PoolOut)),
	}

	// List members so panel isn't just KPIs + empty space.
	var members []watchAccountRow
	for _, a := range s.Accounts {
		if a.InPool {
			members = append(members, a)
		}
	}
	// Prefer live pool status rows when present (pin/cool flags).
	type poolRow struct {
		id, badge string
		cool      bool
	}
	var fromStatus []poolRow
	if s.Status != nil {
		for _, p := range s.Status.Pool {
			id, _ := p["id"].(string)
			if id == "" {
				continue
			}
			cooling, _ := p["cooling"].(bool)
			preferred, _ := p["preferred"].(bool)
			lastUsed, _ := p["last_used"].(bool)
			badge := "idle"
			if cooling {
				badge = "cool"
			} else if preferred {
				badge = "pin"
			} else if lastUsed && s.Mode == "provider" {
				badge = "live"
			}
			fromStatus = append(fromStatus, poolRow{id: id, badge: badge, cool: cooling})
		}
	}

	budget := maxInt(h-6, 0) // chrome + KPI lines
	if budget > 0 {
		lines = append(lines, "")
		shown := 0
		if len(fromStatus) > 0 {
			for _, p := range fromStatus {
				if shown >= budget {
					break
				}
				badgeStyle := rowDim
				switch p.badge {
				case "live":
					badgeStyle = pillUp
				case "pin":
					badgeStyle = lipgloss.NewStyle().Foreground(colYellow).Bold(true)
				case "cool":
					badgeStyle = pillDown
				}
				lines = append(lines, badgeStyle.Render(fmt.Sprintf("%-4s", p.badge))+" "+
					valStyle.Render(truncateRunes(types.DisplayAccountID(p.id), maxInt(w-12, 8))))
				shown++
			}
			if len(fromStatus) > shown {
				lines = append(lines, rowDim.Render(fmt.Sprintf("  … +%d more", len(fromStatus)-shown)))
			}
		} else {
			for _, a := range members {
				if shown >= budget {
					break
				}
				label := accountPrimaryLabel(a)
				lines = append(lines, lipgloss.NewStyle().Foreground(colYellow).Render("POOL")+" "+
					valStyle.Render(truncateRunes(label, maxInt(w-12, 8))))
				shown++
			}
			if len(members) > shown {
				lines = append(lines, rowDim.Render(fmt.Sprintf("  … +%d more", len(members)-shown)))
			}
		}
		if shown == 0 {
			lines = append(lines, rowDim.Render("(no providers in rotate pool)"))
		}
	}
	return watchPanel("POOL", w, h, colGreen, strings.Join(lines, "\n"))
}

func panelUsage(w, h int, s watchSnap, showSpark bool, periodLabel string) string {
	total := s.TokensIn + s.TokensOut
	title := "USAGE · " + strings.ToUpper(periodLabel)
	lines := []string{
		watchKV("requests", valStyle.Render(commaInt(s.UsageRequests))),
		watchKV("tokens", valStyle.Render(fmt.Sprintf("in %s · out %s", commaInt(s.TokensIn), commaInt(s.TokensOut)))),
		watchKV("total", valStyle.Render(commaInt(total))),
	}
	alertStyle := pillUp
	if s.Alerts > 0 {
		alertStyle = pillWarn
	}
	lines = append(lines, watchKV("alerts", alertStyle.Render(fmt.Sprintf("%d", s.Alerts))+rowDim.Render(fmt.Sprintf(" / %d events", s.AlertTotal))))
	if showSpark {
		lines = append(lines, "", labelStyle.Render("trend  ")+watchSparkline(s.History))
	}
	return watchPanel(title, w, h, colOrange, strings.Join(lines, "\n"))
}

func panelActivityMerged(w, h int, s watchSnap, scroll int) string {
	lw, lh, rw, rh, stacked := activityLayout(w, h)
	// Left: tool flow. Right: full ChatGPT / web reply of focused card.
	left := panelRequestFlow(lw, lh, s, scroll)
	right := panelReplyFull(rw, rh, focusedFlowItem(s, scroll, lw, lh))
	if stacked {
		return lipgloss.JoinVertical(lipgloss.Left, left, right)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
}

func focusedFlowItem(s watchSnap, scroll, w, h int) types.RequestEntry {
	items := toolRequestItems(s)
	if len(items) == 0 {
		return types.RequestEntry{}
	}
	_, nShow, _ := flowViewport(w, h)
	end := len(items) - scroll
	if end < 1 {
		end = 1
	}
	if end > len(items) {
		end = len(items)
	}
	if nShow > 0 && end-nShow > 0 && scroll == 0 {
		// live page: newest card
		return items[end-1]
	}
	return items[end-1]
}

func panelReplyFull(w, h int, r types.RequestEntry) string {
	acct := types.DisplayAccountID(r.Account)
	title := "REPLY"
	if acct != "" {
		title = "REPLY  " + acct
	}
	body := strings.TrimSpace(r.Output)
	if body == "" {
		return watchPanel(title, w, h, colPurple, rowDim.Render("no chatgpt text yet…"))
	}
	return watchPanel(title, w, h, colPurple, body)
}

// activityLayout returns left/right panel sizes. stacked=true → vertical.
func activityLayout(w, h int) (lw, lh, rw, rh int, stacked bool) {
	gap := 1
	switch {
	case w < 70:
		flowH := clampInt((h*50)/100, 8, maxInt(h-8, 6))
		if h < 16 {
			flowH = maxInt(h/2, 6)
		}
		return w, flowH, w, h - flowH, true
	case w < 100:
		lw = (w * 40) / 100
		return lw, h, w - lw - gap, h, false
	default:
		lw = (w * 38) / 100
		return lw, h, w - lw - gap, h, false
	}
}

func panelActivityRight(w, h int, s watchSnap, scroll int) string {
	if h < 12 {
		return panelReqsOnly(w, h, s, scroll)
	}
	topH := h / 2
	if topH < 6 {
		topH = h / 2
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		panelPoolLogs(w, topH, s, scroll),
		panelReqsOnly(w, h-topH, s, scroll),
	)
}

func isPoolLogTag(tag string) bool {
	switch strings.ToUpper(strings.TrimSpace(tag)) {
	case "POOL", "FAIL OVER", "FAILOVER", "ROTATE", "SWITCH":
		return true
	default:
		return false
	}
}

func panelPoolLogs(w, h int, s watchSnap, scroll int) string {
	var items []types.EventEntry
	for _, e := range s.Activity {
		if isPoolLogTag(e.Tag) {
			items = append(items, e)
		}
	}
	limit := maxInt(h-5, 1)
	start := len(items) - limit - scroll
	if start < 0 {
		start = 0
	}
	end := len(items) - scroll
	if end > len(items) {
		end = len(items)
	}
	if end < start {
		end = start
	}
	inner := maxInt(w-6, 12)
	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		rows = append(rows, logRowLip(items[i], inner))
	}
	body := rowDim.Render("—")
	if len(rows) > 0 {
		body = strings.Join(rows, "\n")
		if h >= 8 {
			body += "\n" + rowDim.Render(fmt.Sprintf("%d/%d", len(rows), len(items)))
		}
	}
	return watchPanel("POOL LOGS", w, h, colGreen, body)
}

// flowDensity controls how each tool-request is rendered to fit the viewport.
type flowDensity int

const (
	flowDense   flowDensity = iota // 1 line
	flowCompact                    // 2 lines, no border
	flowFull                       // bordered card
)

func pickFlowDensity(w, h int) flowDensity {
	switch {
	case w < 46 || h < 14:
		return flowDense
	case w < 62 || h < 24:
		return flowCompact
	default:
		return flowFull
	}
}

func flowItemLines(d flowDensity) int {
	switch d {
	case flowDense:
		return 1
	case flowCompact:
		return 2
	default:
		return 7 // rounded border + 4 content lines (args/output)
	}
}

func toolRequestItems(s watchSnap) []types.RequestEntry {
	var items []types.RequestEntry
	for _, r := range s.Requests {
		if isPrivacyRedact(r) || isTestFlowAccount(r.Account) {
			continue
		}
		if isWebReplyAccount(r.Account) && strings.TrimSpace(r.Output) != "" {
			items = append(items, r)
			continue
		}
		if len(r.Tools) == 0 || r.Tools[0] == "privacy" {
			continue
		}
		items = append(items, r)
	}
	return items
}

func isWebReplyAccount(account string) bool {
	a := strings.ToLower(account)
	return strings.Contains(a, "chatgpt") || strings.Contains(a, ":web")
}

func isTestFlowAccount(account string) bool {
	switch strings.ToLower(strings.TrimSpace(account)) {
	case "test-adapter", "cursor-stream", "cursor-pool", "pool:01":
		return true
	default:
		return false
	}
}

// flowViewport returns density + how many items fit in panel height h.
func flowViewport(w, h int) (dens flowDensity, nShow, bodyBudget int) {
	dens = pickFlowDensity(w, h)
	itemH := flowItemLines(dens)
	// chrome: panel border(2) + title(1) + footer(1)
	bodyBudget = maxInt(h-4, 1)
	nShow = bodyBudget / itemH
	if dens == flowFull {
		// reserve 1 line for "N shown · …" footer inside body
		nShow = maxInt((bodyBudget-1)/itemH, 1)
		if nShow > 4 {
			nShow = 4
		}
	} else if dens == flowCompact {
		nShow = maxInt((bodyBudget-1)/itemH, 1)
		if nShow > 6 {
			nShow = 6
		}
	} else {
		nShow = maxInt(bodyBudget-1, 1)
		if nShow > 10 {
			nShow = 10
		}
	}
	if nShow < 1 {
		nShow = 1
		dens = flowDense
	}
	return dens, nShow, bodyBudget
}

// activityMaxScroll is ↑↓ range for Activity LIVE FLOW.
func activityMaxScroll(w, h int, s watchSnap) int {
	lw, lh, _, _, _ := activityLayout(w, h)
	_, nShow, _ := flowViewport(lw, lh)
	n := len(toolRequestItems(s))
	return maxInt(n-nShow, 0)
}

func panelRequestFlow(w, h int, s watchSnap, scroll int) string {
	dens, nShow, bodyBudget := flowViewport(w, h)
	items := toolRequestItems(s)
	if len(items) == 0 {
		return watchPanel("LIVE FLOW", w, h, colCyan, rowDim.Render("no tool requests yet…"))
	}

	maxScroll := maxInt(len(items)-nShow, 0)
	if scroll < 0 {
		scroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}

	// Chronological: oldest of the window first, newest last (bottom).
	// scroll 0 = live page (newest). ↑ older.
	end := len(items) - scroll
	start := end - nShow
	if start < 0 {
		start = 0
	}
	slice := items[start:end]

	itemH := flowItemLines(dens)
	var parts []string
	used := 0
	for i := 0; i < len(slice); i++ {
		if used+itemH > bodyBudget-1 && used > 0 {
			// drop oldest of this window so newest still fits
			parts = parts[1:]
			used -= itemH
		}
		parts = append(parts, requestFlowBox(slice[i], w, dens))
		used += itemH
	}

	body := strings.Join(parts, "\n")
	hint := rowDim.Render(fmt.Sprintf("%d/%d · ↑ older", len(parts), len(items)))
	if maxScroll > 0 {
		hint = rowDim.Render(fmt.Sprintf("%d/%d · -%d", len(parts), len(items), scroll))
	}
	if used < bodyBudget {
		body += "\n" + hint
	}

	return watchPanelBottom("LIVE FLOW", w, h, colCyan, body)
}

func clientLabel(dialect string) string {
	d := strings.ToLower(strings.TrimSpace(dialect))
	switch {
	case d == "claude" || strings.Contains(d, "claude"):
		return "claude"
	case d == "cursor":
		return "cursor"
	case d == "codex":
		return "codex"
	case strings.Contains(d, "antigravity") || d == "agy":
		return "antigravity"
	case d == "chat":
		return "am-chat"
	case d == "" || d == "unknown":
		return "client"
	default:
		return d
	}
}

func requestFlowBox(r types.RequestEntry, width int, dens flowDensity) string {
	ts := watchTS(r.Time)
	client := clientLabel(r.Dialect)
	acct := types.DisplayAccountID(r.Account)
	if acct == "" {
		acct = "pool"
	}

	status := pillUp.Render("ok")
	if r.Error != "" || r.ToolStatus == "err" {
		status = pillDown.Render("err")
	} else if r.ToolStatus != "ok" && r.StopReason != "tool_use" && r.StopReason != "tool_calls" {
		status = pillWarn.Render(truncateRunes(r.StopReason, 8))
		if r.StopReason == "" {
			status = pillWarn.Render("—")
		}
	}
	toolsStyle := pillUp
	switch r.ToolStatus {
	case "err":
		toolsStyle = pillDown
	case "ok":
		toolsStyle = pillUp
	default:
		toolsStyle = pillWarn
	}
	toolNames := strings.Join(r.Tools, ",")
	if toolNames == "" {
		toolNames = "—"
	}

	latStyle := rowDim
	if r.DurationMs > 15000 {
		latStyle = lipgloss.NewStyle().Foreground(colRed)
	} else if r.DurationMs > 5000 {
		latStyle = lipgloss.NewStyle().Foreground(colYellow)
	}

	switch dens {
	case flowDense:
		// one line: time client ✦acct Tools names status ms
		return rowDim.Render(ts) + " " +
			lipgloss.NewStyle().Foreground(colPurple).Render(truncateRunes(client, 8)) + " " +
			lipgloss.NewStyle().Foreground(colCyan).Render("✦"+truncateRunes(acct, 10)) + " " +
			toolsStyle.Render(truncateRunes(toolNames, maxInt(width-42, 6))) + " " +
			status + " " + latStyle.Render(fmt.Sprintf("%dms", r.DurationMs))

	case flowCompact:
		// two lines, no border
		line1 := lipgloss.NewStyle().Foreground(colTextHi).Render(truncateRunes(client, 12)) +
			rowDim.Render(" · "+ts+" · ") + status + " " + latStyle.Render(fmt.Sprintf("%dms", r.DurationMs))
		mid := "Tools"
		detail := strings.TrimSpace(r.Output)
		if detail == "" {
			detail = toolNames
		}
		line2 := lipgloss.NewStyle().Foreground(colBlue).Render("PROXY") +
			rowDim.Render("→") +
			lipgloss.NewStyle().Foreground(colCyan).Render("✦"+truncateRunes(acct, 12)) +
			rowDim.Render("→") +
			toolsStyle.Render(mid) +
			rowDim.Render("→") +
			lipgloss.NewStyle().Foreground(colPurple).Render(truncateRunes(client, 10)) +
			rowDim.Render(" ") +
			toolsStyle.Render(truncateRunes(detail, maxInt(width-36, 8)))
		return line1 + "\n" + line2

	default:
		innerW := maxInt(width-6, 20)
		acctShort := truncateRunes(acct, 16)
		proxyNode := lipgloss.NewStyle().Foreground(colBlue).Bold(true).Render("PROXY")
		aiNode := lipgloss.NewStyle().Foreground(colCyan).Render("✦ " + acctShort)
		clientNode := lipgloss.NewStyle().Foreground(colPurple).Render(client)
		toolsNode := toolsStyle.Render("Tools")
		arrow := rowDim.Render(" → ")
		flow := proxyNode + arrow + aiNode + arrow + toolsNode + arrow + clientNode

		head := lipgloss.NewStyle().Foreground(colTextHi).Bold(true).Render(client) +
			rowDim.Render(" · "+ts+" · ") + status + "  " + latStyle.Render(fmt.Sprintf("%dms", r.DurationMs))
		toolLine := rowDim.Render("tools ") + toolsStyle.Render(truncateRunes(toolNames, maxInt(innerW-8, 8)))

		detail := strings.TrimSpace(r.Output)
		if detail == "" {
			detail = strings.TrimSpace(r.Input)
		}
		if detail == "" {
			detail = "—"
		}
		argLine := rowDim.Render(truncateRunes(strings.ReplaceAll(detail, "\n", " "), innerW))
		lines := []string{head, flow, toolLine, argLine}

		accent := colCyan
		if r.Error != "" || r.ToolStatus == "err" {
			accent = colRed
		} else if r.ToolStatus == "ok" {
			accent = colGreen
		}
		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accent).
			Width(maxInt(width-4, 18)).
			Padding(0, 1).
			Render(strings.Join(lines, "\n"))
	}
}

func logsInnerLimit(h int) int {
	return maxInt(h-4, 1)
}

func logsMaxScroll(h int, s watchSnap) int {
	n := len(s.Activity)
	limit := logsInnerLimit(h)
	if n <= limit {
		return 0
	}
	return n - limit
}

func dashLogsMaxScroll(w, h int, s watchSnap) int {
	if w < 100 {
		slot := maxInt(h/5, 1)
		return logsMaxScroll(maxInt(h-slot*4, 1), s)
	}
	return logsMaxScroll(h, s)
}

func panelLogsOnly(w, h int, s watchSnap, scroll int) string {
	limit := logsInnerLimit(h)
	items := s.Activity
	start := len(items) - limit - scroll
	if start < 0 {
		start = 0
	}
	end := len(items) - scroll
	if end > len(items) {
		end = len(items)
	}
	if end < start {
		end = start
	}
	inner := maxInt(w-6, 20)
	rows := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		rows = append(rows, logRowLip(items[i], inner))
	}
	body := ""
	if len(rows) > 0 {
		body = strings.Join(rows, "\n")
	}
	title := "LOGS  follow"
	if scroll > 0 {
		title = fmt.Sprintf("LOGS  -%d", scroll)
	}
	return watchPanelBottom(title, w, h, colPurple, body)
}

func panelReqsOnly(w, h int, s watchSnap, scroll int) string {
	limit := maxInt(h-5, 4)
	items := s.Requests
	start := len(items) - limit - scroll
	if start < 0 {
		start = 0
	}
	end := len(items) - scroll
	if end > len(items) {
		end = len(items)
	}
	if end < start {
		end = start
	}
	inner := maxInt(w-6, 20)
	rows := make([]string, 0, end-start)
	for i := end - 1; i >= start; i-- {
		rows = append(rows, reqRowLip(items[i]))
		_ = inner
	}
	body := rowDim.Render("—")
	if len(rows) > 0 {
		body = strings.Join(rows, "\n")
		body += "\n" + rowDim.Render(fmt.Sprintf("%d/%d", len(rows), len(items)))
	}
	return watchPanel("REQUEST LOGS", w, h, colCyan, body)
}

func logRowLip(e types.EventEntry, maxW int) string {
	tagColor := colPurple
	switch strings.ToUpper(e.Tag) {
	case "PROXY":
		tagColor = colBlue
	case "POOL", "FAIL OVER", "FAILOVER":
		tagColor = colGreen
	case "TOOLS":
		tagColor = colCyan
	case "ERROR", "AUTH":
		tagColor = colRed
	case "WARN", "DEGRADED":
		tagColor = colYellow
	}
	msg := truncateRunes(types.RemapAccountIDsInText(e.Message), maxInt(maxW-24, 8))
	return rowDim.Render(watchTS(e.Time)) + "  " +
		lipgloss.NewStyle().Foreground(tagColor).Render(fmt.Sprintf("%-8s", e.Tag)) +
		valStyle.Render(msg)
}

func reqRowLip(r types.RequestEntry) string {
	statusStyle := pillUp
	st := "ok"
	if r.Error != "" {
		statusStyle = pillDown
		st = "err"
	}
	latStyle := rowDim
	if r.DurationMs > 15000 {
		latStyle = lipgloss.NewStyle().Foreground(colRed)
	} else if r.DurationMs > 5000 {
		latStyle = lipgloss.NewStyle().Foreground(colYellow)
	}
	acct := types.DisplayAccountID(r.Account)
	if acct == "" {
		acct = "-"
	}
	kind := clientLabel(r.Dialect)
	return rowDim.Render(watchTS(r.Time)) + "  " +
		statusStyle.Render(fmt.Sprintf("%-3s", st)) + "  " +
		valStyle.Render(fmt.Sprintf("%-8s", truncateRunes(kind, 8))) + " " +
		lipgloss.NewStyle().Foreground(colCyan).Render(fmt.Sprintf("%-14s", truncateRunes(acct, 14))) + " " +
		latStyle.Render(fmt.Sprintf("%6dms", r.DurationMs))
}

// usageNav is selection state for the Usage tab drill-down.
type usageNav struct {
	Level   usageLevel
	Scope   string // account or project name
	IsProj  bool
	Day     time.Time // selected day (local midnight)
	Cursor  int
	Scroll  int
	Period  usagePeriod
}

func panelUsageDetail(w, h int, s watchSnap, nav usageNav) string {
	day := nav.Day
	if day.IsZero() {
		day = startOfDay(time.Now())
	} else {
		day = startOfDay(day)
	}
	nav.Day = day

	switch nav.Level {
	case usageDays:
		return panelUsageDays(w, h, s, nav)
	case usageRequests:
		return panelUsageRequests(w, h, s, nav)
	default:
		return panelUsageOverview(w, h, s, nav)
	}
}

func panelUsageOverview(w, h int, s watchSnap, nav usageNav) string {
	sum := rowDim.Render(fmt.Sprintf("%s · in %s · out %s · %d req",
		strings.ToUpper(periodLabels[nav.Period]),
		commaInt(s.TokensIn),
		commaInt(s.TokensOut),
		s.UsageRequests,
	))
	hint := rowDim.Render("↑↓ select · Enter open · p project · d/w/m period")

	// Build selectable rows: accounts then projects
	type sel struct {
		kind string // "acct" | "proj"
		name string
		a    *tokAgg
	}
	var list []sel
	for _, r := range s.UsageByAccount {
		list = append(list, sel{"acct", r.name, r.a})
	}
	for _, r := range s.UsageByProject {
		if r.name == "(none)" {
			continue
		}
		list = append(list, sel{"proj", r.name, r.a})
	}
	if len(list) == 0 {
		msg := "no usage in this period"
		if nav.Period == periodDay {
			msg = "no usage today — press w (week) or m (month)"
		}
		return sum + "\n" + watchPanel("USAGE", w, h-2, colOrange, rowDim.Render(msg))
	}
	cur := nav.Cursor
	if cur < 0 {
		cur = 0
	}
	if cur >= len(list) {
		cur = len(list) - 1
	}

	limit := maxInt(h-5, 4)
	start := 0
	if cur >= limit {
		start = cur - limit + 1
	}
	end := start + limit
	if end > len(list) {
		end = len(list)
	}

	var lines []string
	var lastKind string
	for i := start; i < end; i++ {
		it := list[i]
		if it.kind != lastKind {
			lastKind = it.kind
			title := "ACCOUNTS"
			if it.kind == "proj" {
				title = "PROJECTS"
			}
			lines = append(lines, lipgloss.NewStyle().Foreground(colOrange).Bold(true).Render(title))
		}
		marker := "  "
		style := valStyle
		if i == cur {
			marker = "▸ "
			style = lipgloss.NewStyle().Foreground(colTextHi).Bold(true)
		}
		label := it.name
		if it.kind == "proj" {
			label = "proj:" + it.name
		}
		lines = append(lines, marker+style.Render(fmt.Sprintf("%-18s", truncateRunes(label, 18)))+
			rowDim.Render(fmt.Sprintf("  in %s  out %s  · %d req",
				commaInt(it.a.in), commaInt(it.a.out), it.a.n)))
	}
	body := strings.Join(lines, "\n")
	body += "\n" + rowDim.Render(fmt.Sprintf("%d/%d", cur+1, len(list)))
	return sum + "\n" + hint + "\n" + watchPanel("USAGE", w, h-3, colOrange, body)
}

func filterUsageScope(entries []types.UsageEntry, nav usageNav) []types.UsageEntry {
	var out []types.UsageEntry
	for _, e := range entries {
		if nav.IsProj {
			if !strings.EqualFold(usage.ProjectLabel(e.Project), nav.Scope) {
				continue
			}
		} else {
			acct := e.Account
			if acct == "" {
				acct = "(unknown)"
			}
			if !strings.EqualFold(acct, nav.Scope) {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

func aggregateUsageDays(entries []types.UsageEntry, period usagePeriod, anchor time.Time) []dayAgg {
	anchor = startOfDay(anchor)
	nDays := 1
	switch period {
	case periodWeek:
		nDays = 7
	case periodMonth:
		nDays = 30
	}
	// Oldest → newest
	days := make([]dayAgg, nDays)
	for i := 0; i < nDays; i++ {
		d := anchor.AddDate(0, 0, -(nDays - 1 - i))
		days[i] = dayAgg{Day: d}
	}
	idx := map[string]int{}
	for i, d := range days {
		idx[d.Day.Format("2006-01-02")] = i
	}
	for _, e := range entries {
		k := startOfDay(e.Time).Format("2006-01-02")
		i, ok := idx[k]
		if !ok {
			continue
		}
		days[i].In += e.Input
		days[i].Out += e.Output
		days[i].N++
	}
	return days
}

func panelUsageDays(w, h int, s watchSnap, nav usageNav) string {
	scoped := filterUsageScope(s.UsageEntries, nav)
	days := aggregateUsageDays(scoped, nav.Period, time.Now())
	scopeLabel := nav.Scope
	if nav.IsProj {
		scopeLabel = "proj:" + nav.Scope
	}
	var tin, tout, tn int
	for _, d := range days {
		tin += d.In
		tout += d.Out
		tn += d.N
	}
	sum := rowDim.Render(fmt.Sprintf("%s · %s · %d days · in %s · out %s · %d req",
		scopeLabel,
		strings.ToUpper(periodLabels[nav.Period]),
		len(days),
		commaInt(tin), commaInt(tout), tn,
	))
	hint := rowDim.Render("↑↓ day · Enter requests · Esc back · d/w/m")

	cur := nav.Cursor
	if cur < 0 {
		cur = 0
	}
	if cur >= len(days) {
		cur = len(days) - 1
	}
	// Default cursor to today (last day)
	if nav.Cursor == 0 && len(days) > 0 {
		// keep as-is; openDays sets cursor to last
	}

	limit := maxInt(h-5, 4)
	start := 0
	if cur >= limit {
		start = cur - limit + 1
	}
	end := start + limit
	if end > len(days) {
		end = len(days)
	}

	today := startOfDay(time.Now())
	var lines []string
	// newest first for display
	for i := end - 1; i >= start; i-- {
		d := days[i]
		marker := "  "
		style := valStyle
		if i == cur {
			marker = "▸ "
			style = lipgloss.NewStyle().Foreground(colTextHi).Bold(true)
		}
		label := d.Day.Format("Mon 2006-01-02")
		if sameDay(d.Day, today) {
			label += " (today)"
		}
		lines = append(lines, marker+style.Render(fmt.Sprintf("%-22s", label))+
			rowDim.Render(fmt.Sprintf("  %3d req  in %s  out %s",
				d.N, commaInt(d.In), commaInt(d.Out))))
	}
	body := strings.Join(lines, "\n")
	if body == "" {
		body = rowDim.Render("no days")
	}
	return sum + "\n" + hint + "\n" + watchPanel("BY DAY", w, h-3, colOrange, body)
}

func panelUsageRequests(w, h int, s watchSnap, nav usageNav) string {
	day := startOfDay(nav.Day)
	if day.IsZero() {
		day = startOfDay(time.Now())
	}
	scoped := filterUsageScope(s.UsageEntries, nav)
	var dayEntries []types.UsageEntry
	var tin, tout int
	for _, e := range scoped {
		if sameDay(e.Time, day) {
			dayEntries = append(dayEntries, e)
			tin += e.Input
			tout += e.Output
		}
	}
	scopeLabel := nav.Scope
	if nav.IsProj {
		scopeLabel = "proj:" + nav.Scope
	}
	sum := rowDim.Render(fmt.Sprintf("%s · %s · %d req · in %s · out %s",
		scopeLabel,
		day.Format("2006-01-02"),
		len(dayEntries),
		commaInt(tin), commaInt(tout),
	))
	hint := rowDim.Render("[ ] prev/next day · Esc back · ↑↓ scroll")

	limit := maxInt(h-5, 3)
	scroll := nav.Scroll
	if scroll < 0 {
		scroll = 0
	}
	maxScroll := maxInt(len(dayEntries)-limit, 0)
	if scroll > maxScroll {
		scroll = maxScroll
	}
	// newest last in file → show newest first
	end := len(dayEntries) - scroll
	if end > len(dayEntries) {
		end = len(dayEntries)
	}
	start := end - limit
	if start < 0 {
		start = 0
	}

	var lines []string
	for i := end - 1; i >= start; i-- {
		e := dayEntries[i]
		model := e.Model
		if model == "" {
			model = "-"
		}
		lines = append(lines,
			rowDim.Render(watchTS(e.Time))+"  "+
				valStyle.Render(fmt.Sprintf("in %-7s", commaInt(e.Input)))+"  "+
				valStyle.Render(fmt.Sprintf("out %-7s", commaInt(e.Output)))+"  "+
				rowDim.Render(truncateRunes(model, maxInt(w-48, 8))),
		)
	}
	body := rowDim.Render("no requests this day")
	if len(lines) > 0 {
		body = strings.Join(lines, "\n")
		body += "\n" + rowDim.Render(fmt.Sprintf("%d/%d", len(lines), len(dayEntries)))
	}
	return sum + "\n" + hint + "\n" + watchPanel("REQUESTS", w, h-3, colOrange, body)
}

func usageOverviewLen(s watchSnap) int {
	n := len(s.UsageByAccount)
	for _, r := range s.UsageByProject {
		if r.name != "(none)" {
			n++
		}
	}
	return n
}

func usageOverviewPick(s watchSnap, cursor int) (name string, isProj bool, ok bool) {
	var list []struct {
		name   string
		isProj bool
	}
	for _, r := range s.UsageByAccount {
		list = append(list, struct {
			name   string
			isProj bool
		}{r.name, false})
	}
	for _, r := range s.UsageByProject {
		if r.name == "(none)" {
			continue
		}
		list = append(list, struct {
			name   string
			isProj bool
		}{r.name, true})
	}
	if cursor < 0 || cursor >= len(list) {
		return "", false, false
	}
	return list[cursor].name, list[cursor].isProj, true
}

func usageDaysCount(period usagePeriod) int {
	switch period {
	case periodWeek:
		return 7
	case periodMonth:
		return 30
	default:
		return 1
	}
}

func panelActivity(w, h int, s watchSnap, limit int) string {
	items := s.Activity
	if len(items) > limit {
		items = items[len(items)-limit:]
	}
	rows := make([]string, 0, len(items))
	for i := len(items) - 1; i >= 0; i-- {
		rows = append(rows, logRowLip(items[i], w-4))
	}
	body := rowDim.Render("—")
	if len(rows) > 0 {
		body = strings.Join(rows, "\n")
	}
	return watchPanel("ACTIVITY", w, h, colPurple, body)
}

func panelRequests(w, h int, s watchSnap, limit int) string {
	items := s.Requests
	if len(items) > limit {
		items = items[len(items)-limit:]
	}
	rows := make([]string, 0, len(items))
	for i := len(items) - 1; i >= 0; i-- {
		rows = append(rows, reqRowLip(items[i]))
	}
	body := rowDim.Render("—")
	if len(rows) > 0 {
		body = strings.Join(rows, "\n")
	}
	return watchPanel("REQUESTS", w, h, colCyan, body)
}

func truncateRunes(s string, n int) string {
	return monitor.TruncateRunes(s, n)
}
