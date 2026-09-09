package usage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"amux-accounts/pkg/types"
)

func LoadUsageEntries(from time.Time) []types.UsageEntry {
	f, err := os.Open(UsageLogPath())
	if err != nil {
		return nil
	}
	defer f.Close()

	var out []types.UsageEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e types.UsageEntry
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		if !from.IsZero() && e.Time.Before(from) {
			continue
		}
		out = append(out, e)
	}
	return out
}

type usageAgg struct {
	in, out int
	reqs    int
}

// PrintUsageReport parses flags and prints either the daily aggregate table or
// the detailed breakdown by account/model/project/session.
func PrintUsageReport(args []string) {
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
				fmt.Fprintln(os.Stderr, "amux usage: --date/-d needs a value (YYYY-MM-DD)")
				os.Exit(1)
			}
			dateArg = args[i]
		case "--project", "-p":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "amux usage: --project/-p needs a value")
				os.Exit(1)
			}
			projectFilter = args[i]
		default:
			if periodGiven || strings.HasPrefix(a, "-") {
				fmt.Fprintln(os.Stderr, "amux usage: [day|week|month|all] [-d|--date YYYY-MM-DD] [-p|--project NAME] [-D|--detail]")
				os.Exit(1)
			}
			period = a
			periodGiven = true
		}
	}

	now := time.Now()
	var since time.Time
	var dateOnly time.Time
	if dateArg != "" {
		d, err := time.ParseInLocation("2006-01-02", dateArg, now.Location())
		if err != nil {
			fmt.Fprintf(os.Stderr, "amux usage: --date %q: want YYYY-MM-DD\n", dateArg)
			os.Exit(1)
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
			fmt.Fprintln(os.Stderr, "amux usage: [day|week|month|all] [--date YYYY-MM-DD] [--project NAME] [--detail]")
			os.Exit(1)
		}
	}

	entries := LoadUsageEntries(since)
	var filtered []types.UsageEntry
	for _, e := range entries {
		if !since.IsZero() && e.Time.Before(since) {
			continue
		}
		if !dateOnly.IsZero() && e.Time.After(dateOnly.AddDate(0, 0, 1)) {
			continue
		}
		if projectFilter != "" && ProjectLabel(e.Project) != projectFilter {
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

func printUsageByDay(entries []types.UsageEntry) {
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
		fmt.Printf("%-12s  %12s  %12s  %8d\n", d, Commas(a.in), Commas(a.out), a.reqs)
	}
	fmt.Println(strings.Repeat("-", 48))
	fmt.Printf("%-12s  %12s  %12s  %8d\n", "total", Commas(totalIn), Commas(totalOut), totalReqs)
}

func printUsageDetail(entries []types.UsageEntry) {
	byAccount := map[string]*usageAgg{}
	byModel := map[string]*usageAgg{}
	byProject := map[string]*usageAgg{}
	bySession := map[string]*usageAgg{}
	lastSeen := map[string]time.Time{}
	var totalIn, totalOut, totalReqs int
	for _, e := range entries {
		acct := e.Account
		if acct == "" {
			acct = "-"
		}
		model := e.Model
		if model == "" {
			model = "-"
		}
		bump(byAccount, acct, e)
		bump(byModel, model, e)
		bump(byProject, ProjectLabel(e.Project), e)
		sid := SessionLabel(e.Session)
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
	fmt.Printf("%-30s  in %9s   out %9s   %5d req\n", "total", Commas(totalIn), Commas(totalOut), totalReqs)
}

func ProjectLabel(dir string) string {
	if dir == "" {
		return "-"
	}
	return filepath.Base(dir)
}

func SessionLabel(id string) string {
	if id == "" {
		return "-"
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func bump(m map[string]*usageAgg, key string, e types.UsageEntry) {
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
		fmt.Printf("  %-28s  in %9s   out %9s   %5d req\n", n, Commas(a.in), Commas(a.out), a.reqs)
	}
}

func printUsageTableByTime(m map[string]*usageAgg, lastSeen map[string]time.Time) {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return lastSeen[names[i]].After(lastSeen[names[j]]) })
	for _, n := range names {
		a := m[n]
		fmt.Printf("  %-28s  in %9s   out %9s   %5d req   last %s\n",
			n, Commas(a.in), Commas(a.out), a.reqs, lastSeen[n].Local().Format("01-02 15:04"))
	}
}

func Commas(n int) string {
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
