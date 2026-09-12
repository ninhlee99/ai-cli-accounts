package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"amux-accounts/pkg/monitor"
	"amux-accounts/pkg/term"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	goterm "golang.org/x/term"
)

var watchTabs = []string{"Dash", "Accounts", "Usage", "Activity"}

type watchTickMsg time.Time

func watchTickCmd() tea.Cmd {
	return tea.Tick(800*time.Millisecond, func(t time.Time) tea.Msg {
		return watchTickMsg(t)
	})
}

type watchModel struct {
	snap      watchSnap
	activeTab int
	width     int
	height    int

	filter   string
	editMode string // "", "filter", "project"
	editBuf  string
	scroll   int

	uPeriod  usagePeriod
	uProject string
	uLevel   usageLevel
	uScope   string
	uIsProj  bool
	uDay     time.Time
	uCursor  int
	uScroll  int // request list scroll inside day detail
}

func newWatchModel() watchModel {
	m := watchModel{
		uPeriod: periodWeek, // default: last 7 days (not only today)
		uDay:    startOfDay(time.Now()),
	}
	m.refresh()
	return m
}

func (m *watchModel) refresh() {
	filter := m.filter
	// Dash keeps unfiltered KPIs; other tabs honor filter.
	if m.activeTab == int(tabDash) {
		filter = ""
	}
	// When drilled into a project scope, still load broader set unless uProject set via `p`.
	m.snap = loadWatchSnap(m.uPeriod, m.uProject, filter)
}

func (m *watchModel) usageNav() usageNav {
	day := m.uDay
	if day.IsZero() {
		day = startOfDay(time.Now())
	}
	return usageNav{
		Level:  m.uLevel,
		Scope:  m.uScope,
		IsProj: m.uIsProj,
		Day:    day,
		Cursor: m.uCursor,
		Scroll: m.uScroll,
		Period: m.uPeriod,
	}
}

func (m watchModel) Init() tea.Cmd {
	return watchTickCmd()
}

func (m watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.clampScroll()
		return m, nil

	case watchTickMsg:
		m.refresh()
		m.clampScroll()
		return m, watchTickCmd()

	case tea.KeyMsg:
		key := msg.String()
		if m.editMode != "" {
			return m.updateEdit(key)
		}
		if m.activeTab == int(tabUsage) {
			if handled := m.updateUsageKey(key); handled {
				return m, nil
			}
		}
		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "1", "2", "3", "4":
			m.activeTab = int(key[0] - '1')
			m.scroll = 0
			m.resetUsageNavSoft()
			m.refresh()
		case "tab", "right":
			m.activeTab = (m.activeTab + 1) % len(watchTabs)
			m.scroll = 0
			m.resetUsageNavSoft()
			m.refresh()
		case "shift+tab", "left":
			m.activeTab = (m.activeTab - 1 + len(watchTabs)) % len(watchTabs)
			m.scroll = 0
			m.resetUsageNavSoft()
			m.refresh()
		case "r":
			m.refresh()
		case "/":
			if m.activeTab != int(tabDash) {
				m.editMode = "filter"
				m.editBuf = m.filter
			}
		case "c":
			m.filter = ""
			if m.activeTab == int(tabUsage) {
				m.uProject = ""
				m.uLevel = usageOverview
				m.uScope = ""
				m.uCursor = 0
			}
			m.scroll = 0
			m.refresh()
		case "up", "k":
			if m.scroll > 0 {
				m.scroll--
			}
		case "down", "j":
			m.scroll++
			m.clampScroll()
		case "pgup":
			m.scroll -= 3
			if m.scroll < 0 {
				m.scroll = 0
			}
		case "pgdown":
			m.scroll += 3
			m.clampScroll()
		}
		return m, nil
	}
	return m, nil
}

func (m *watchModel) resetUsageNavSoft() {
	// Keep period; reset drill when leaving Usage.
	if m.activeTab != int(tabUsage) {
		m.uLevel = usageOverview
		m.uScope = ""
		m.uCursor = 0
		m.uScroll = 0
	}
}

func (m *watchModel) updateUsageKey(key string) bool {
	switch key {
	case "p":
		m.editMode = "project"
		m.editBuf = m.uProject
		return true
	case "d":
		m.uPeriod = periodDay
		m.uDay = startOfDay(time.Now())
		m.uScroll = 0
		if m.uLevel == usageDays {
			// day period → jump straight to today's requests
			m.uLevel = usageRequests
			m.uCursor = 0
		}
		m.refresh()
		return true
	case "w":
		m.uPeriod = periodWeek
		m.uScroll = 0
		if m.uLevel == usageRequests && m.uScope != "" {
			m.uLevel = usageDays
			m.uCursor = usageDaysCount(periodWeek) - 1
		}
		m.refresh()
		return true
	case "m":
		m.uPeriod = periodMonth
		m.uScroll = 0
		if m.uLevel == usageRequests && m.uScope != "" {
			m.uLevel = usageDays
			m.uCursor = usageDaysCount(periodMonth) - 1
		}
		m.refresh()
		return true
	case "esc", "backspace":
		switch m.uLevel {
		case usageRequests:
			if m.uPeriod == periodDay {
				m.uLevel = usageOverview
				m.uScope = ""
			} else {
				m.uLevel = usageDays
				m.uCursor = usageDaysCount(m.uPeriod) - 1
			}
			m.uScroll = 0
			return true
		case usageDays:
			m.uLevel = usageOverview
			m.uScope = ""
			m.uCursor = 0
			return true
		}
		return false
	case "enter":
		switch m.uLevel {
		case usageOverview:
			name, isProj, ok := usageOverviewPick(m.snap, m.uCursor)
			if !ok {
				return true
			}
			m.uScope = name
			m.uIsProj = isProj
			m.uScroll = 0
			if m.uPeriod == periodDay {
				m.uDay = startOfDay(time.Now())
				m.uLevel = usageRequests
			} else {
				m.uLevel = usageDays
				m.uCursor = usageDaysCount(m.uPeriod) - 1 // today
			}
			return true
		case usageDays:
			n := usageDaysCount(m.uPeriod)
			if n == 0 {
				return true
			}
			cur := m.uCursor
			if cur < 0 {
				cur = 0
			}
			if cur >= n {
				cur = n - 1
			}
			anchor := startOfDay(time.Now())
			m.uDay = anchor.AddDate(0, 0, -(n - 1 - cur))
			m.uLevel = usageRequests
			m.uScroll = 0
			return true
		}
		return true
	case "up", "k":
		switch m.uLevel {
		case usageOverview:
			if m.uCursor > 0 {
				m.uCursor--
			}
		case usageDays:
			if m.uCursor > 0 {
				m.uCursor--
			}
		case usageRequests:
			m.uScroll++
		}
		return true
	case "down", "j":
		switch m.uLevel {
		case usageOverview:
			max := usageOverviewLen(m.snap) - 1
			if m.uCursor < max {
				m.uCursor++
			}
		case usageDays:
			max := usageDaysCount(m.uPeriod) - 1
			if m.uCursor < max {
				m.uCursor++
			}
		case usageRequests:
			if m.uScroll > 0 {
				m.uScroll--
			}
		}
		return true
	case "[":
		if m.uLevel == usageRequests {
			m.uDay = startOfDay(m.uDay).AddDate(0, 0, -1)
			oldest := startOfDay(time.Now()).AddDate(0, 0, -29)
			if m.uDay.Before(oldest) {
				m.uDay = oldest
			}
			m.uScroll = 0
		}
		return true
	case "]":
		if m.uLevel == usageRequests {
			next := startOfDay(m.uDay).AddDate(0, 0, 1)
			today := startOfDay(time.Now())
			if next.After(today) {
				next = today
			}
			m.uDay = next
			m.uScroll = 0
		}
		return true
	}
	return false
}

func (m *watchModel) clampScroll() {
	w, h := m.contentSize()
	max := 0
	switch m.activeTab {
	case int(tabActivity):
		max = activityMaxScroll(w, h, m.snap)
	case int(tabAccounts):
		max = 50
	case int(tabUsage):
		switch m.uLevel {
		case usageOverview:
			max = maxInt(usageOverviewLen(m.snap)-1, 0)
			if m.uCursor > max {
				m.uCursor = max
			}
		case usageDays:
			max = maxInt(usageDaysCount(m.uPeriod)-1, 0)
			if m.uCursor > max {
				m.uCursor = max
			}
		}
		return
	}
	if m.scroll > max {
		m.scroll = max
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

func (m watchModel) updateEdit(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "ctrl+c":
		m.editMode = ""
	case "enter":
		if m.editMode == "filter" {
			m.filter = m.editBuf
		} else if m.editMode == "project" {
			m.uProject = strings.TrimSpace(m.editBuf)
			m.uLevel = usageOverview
			m.uScope = ""
			m.uCursor = 0
		}
		m.editMode = ""
		m.scroll = 0
		m.refresh()
	case "backspace":
		if m.editBuf != "" {
			r := []rune(m.editBuf)
			m.editBuf = string(r[:len(r)-1])
		}
	default:
		if len(key) == 1 && key[0] >= 32 && key[0] < 127 {
			m.editBuf += key
		}
	}
	return m, nil
}

func (m watchModel) contentSize() (int, int) {
	w := m.width
	h := m.height - 3 /*header*/ - 1 /*footer*/
	if m.activeTab != int(tabDash) {
		h -= 1 // filter chrome row
	}
	if w < 60 {
		w = 60
	}
	if h < 10 {
		h = 10
	}
	return w, h
}

func (m watchModel) View() string {
	if m.width == 0 {
		return "booting amux…"
	}
	w, h := m.contentSize()
	var body string
	switch m.activeTab {
	case int(tabDash):
		body = m.viewDash(w, h)
	case int(tabAccounts):
		body = panelAccountsGrouped(w, h, m.snap, m.scroll)
	case int(tabUsage):
		body = panelUsageDetail(w, h, m.snap, m.usageNav())
	case int(tabActivity):
		body = panelActivityMerged(w, h, m.snap, m.scroll)
	}
	full := lipgloss.JoinVertical(lipgloss.Left,
		m.renderHeader(),
		// Fixed height so keyboard footer always sticks to screen bottom.
		lipgloss.NewStyle().Width(m.width).Height(h).Align(lipgloss.Left, lipgloss.Top).Render(body),
		m.renderFooter(),
	)
	return appStyle.Render(full)
}

func (m watchModel) renderHeader() string {
	brand := brandStyle.Render("amux") + brandSubStyle.Render("  gateway · rotate · pool · watch")
	var tabs []string
	for i, t := range watchTabs {
		label := fmt.Sprintf(" %d:%s ", i+1, t)
		if i == m.activeTab {
			tabs = append(tabs, tabActive.Render(label))
		} else {
			tabs = append(tabs, tabIdle.Render(label))
		}
	}
	tabRow := lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
	gap := m.width - lipgloss.Width(brand) - lipgloss.Width(tabRow) - 4
	if gap < 1 {
		gap = 1
	}
	line := brand + strings.Repeat(" ", gap) + tabRow
	head := headerBar.Width(m.width).Render(line)

	if m.activeTab == int(tabDash) {
		return head
	}
	fDisp := m.filter
	if m.editMode == "filter" {
		fDisp = m.editBuf + "▏"
	}
	if fDisp == "" {
		fDisp = "(all)"
	}
	extra := rowDim.Render("filter ") + valStyle.Render(fDisp)
	if m.activeTab == int(tabUsage) {
		extra += "  ·  "
		for i, lab := range periodLabels {
			if usagePeriod(i) == m.uPeriod {
				extra += pillUp.Render(lab) + " "
			} else {
				extra += rowDim.Render(lab) + " "
			}
		}
		if m.uScope != "" {
			lab := m.uScope
			if m.uIsProj {
				lab = "proj:" + m.uScope
			}
			extra += " · " + pillWarn.Render(lab)
		}
		if m.uLevel == usageRequests {
			extra += " · " + valStyle.Render(startOfDay(m.uDay).Format("2006-01-02"))
		}
		pDisp := m.uProject
		if m.editMode == "project" {
			pDisp = m.editBuf + "▏"
		}
		if pDisp == "" {
			pDisp = "all"
		}
		extra += " · " + rowDim.Render("project ") + valStyle.Render(pDisp)
	}
	if m.activeTab == int(tabAccounts) {
		extra += "  ·  " + rowDim.Render("↑↓ scroll · limits live via proxy (~0.8s)")
	}
	return lipgloss.JoinVertical(lipgloss.Left, head, headerBar.Width(m.width).Render(extra))
}

func (m watchModel) renderFooter() string {
	sep := rowDim.Render("  ·  ")
	var sections []string
	if m.editMode != "" {
		sections = []string{
			keyStyle.Render("Enter") + " apply",
			keyStyle.Render("Esc") + " cancel",
		}
	} else {
		sections = []string{
			keyStyle.Render("1-4") + " tab",
			keyStyle.Render("Tab") + " cycle",
			keyStyle.Render("r") + " refresh",
			keyStyle.Render("q") + " quit",
		}
		switch m.activeTab {
		case int(tabAccounts), int(tabActivity):
			sections = append([]string{keyStyle.Render("↑↓/jk") + " scroll", keyStyle.Render("/") + " filter", keyStyle.Render("c") + " clear"}, sections...)
		case int(tabUsage):
			sections = append([]string{
				keyStyle.Render("Enter") + " open",
				keyStyle.Render("Esc") + " back",
				keyStyle.Render("d/w/m") + " period",
				keyStyle.Render("[]") + " day",
				keyStyle.Render("p") + " project",
			}, sections...)
		}
	}
	line := strings.Join(sections, sep)
	return footerBar.Width(m.width).Render(line)
}

func (m watchModel) viewDash(w, h int) string {
	if w < 100 {
		half := h / 3
		return lipgloss.JoinVertical(lipgloss.Left,
			panelProxy(w, half, m.snap),
			panelAccounts(w, half, m.snap),
			panelPool(w, half, m.snap),
			panelUsage(w, half, m.snap, false, periodLabels[m.uPeriod]),
			panelActivity(w, half, m.snap, half-4),
			panelRequests(w, half, m.snap, half-4),
		)
	}
	colW := (w - 4) / 3
	rowH := h / 2
	col1 := lipgloss.JoinVertical(lipgloss.Left,
		panelProxy(colW, rowH-2, m.snap),
		panelAccounts(colW, h-(rowH-2), m.snap),
	)
	col2 := lipgloss.JoinVertical(lipgloss.Left,
		panelPool(colW, rowH-2, m.snap),
		panelUsage(colW, h-(rowH-2), m.snap, false, "7d"),
	)
	col3 := lipgloss.JoinVertical(lipgloss.Left,
		panelActivity(colW, rowH-2, m.snap, rowH-4),
		panelRequests(colW, h-(rowH-2), m.snap, h-rowH-2),
	)
	return lipgloss.JoinHorizontal(lipgloss.Top, col1, " ", col2, " ", col3)
}

// CmdWatch runs the Bubble Tea monitoring dashboard.
func CmdWatch() {
	monitor.EnableTermSink()
	fd := int(os.Stdin.Fd())
	if !goterm.IsTerminal(fd) {
		term.Warn("amux watch needs an interactive terminal")
		return
	}
	p := tea.NewProgram(newWatchModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		term.Error("watch: %v", err)
	}
}
