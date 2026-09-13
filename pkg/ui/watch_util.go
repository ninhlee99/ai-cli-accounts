package ui

import (
	"time"

	"amux-accounts/pkg/types"
)

// isPrivacyRedact reports a privacy-redact log row. Accepts the old
// "privacy_scrub" stop reason so historical ~/.am/requests.log still shows.
func isPrivacyRedact(r types.RequestEntry) bool {
	if len(r.Redactions) > 0 {
		return true
	}
	switch r.StopReason {
	case "privacy_redact", "privacy_scrub":
		return true
	}
	return len(r.Tools) > 0 && r.Tools[0] == "privacy"
}

type watchTab int

const (
	tabDash watchTab = iota
	tabAccounts
	tabUsage
	tabActivity
	tabCount
)

type usagePeriod int

const (
	periodDay usagePeriod = iota
	periodWeek
	periodMonth
)

var periodLabels = []string{"day", "week", "month"}

// usageLevel: overview → days → requests.
type usageLevel int

const (
	usageOverview usageLevel = iota
	usageDays
	usageRequests
)

type tokAgg struct{ in, out, n int }

type aggRow struct {
	name string
	a    *tokAgg
}

type dayAgg struct {
	Day time.Time
	In  int
	Out int
	N   int
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
		return now.AddDate(0, 0, -6) // 7 days incl today
	case periodMonth:
		return now.AddDate(0, 0, -29) // 30 days max
	default:
		return now.AddDate(0, 0, -29)
	}
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.In(time.Local).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

func sameDay(a, b time.Time) bool {
	return startOfDay(a).Equal(startOfDay(b))
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
	end := n - scroll
	start := end - page
	if start < 0 {
		start = 0
	}
	return viewRange{from: start, to: end}
}

func (v viewRange) len() int {
	if v.to <= v.from {
		return 0
	}
	return v.to - v.from
}
