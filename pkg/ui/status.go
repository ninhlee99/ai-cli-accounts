package ui

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"amux-accounts/pkg/hook"
	"amux-accounts/pkg/profile"
	"amux-accounts/pkg/proxy"
	"amux-accounts/pkg/term"
)

// proxyStatus is the decoded /_am/status payload (shared by status + watch Dash).
type proxyStatus struct {
	Tool     string `json:"tool"`
	Switches int    `json:"switches"`
	Sessions int    `json:"sessions"`
	Upstream string `json:"upstream"`
	Mode     string `json:"mode"`
	Accounts []struct {
		Profile        string   `json:"profile"`
		Account        string   `json:"account"`
		Active         bool     `json:"active"`
		Remaining      float64  `json:"remaining"`
		LimitReset     string   `json:"limit_reset"`
		Cooldown       string   `json:"cooldown_until"`
		Dead           bool     `json:"dead"`
		Disabled       bool     `json:"disabled"`
		FiveHUsed      *float64 `json:"5h_used"`
		FiveHReset     string   `json:"5h_reset"`
		SevenDUsed     *float64 `json:"7d_used"`
		SevenDReset    string   `json:"7d_reset"`
		AutoSwitches   int      `json:"auto_switches"`
		ManualSwitches int      `json:"manual_switches"`
	} `json:"accounts"`
	Pool []map[string]any `json:"pool"`
}

func fetchProxyStatus() (*proxyStatus, error) {
	resp, err := http.Get(proxy.ProxyBase() + "/_am/status")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var s proxyStatus
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}

// CmdStatus displays a modern realtime dashboard for proxy, Claude accounts,
// and the multi-provider pool.
func CmdStatus() {
	term.Header("amux status", "live gateway · accounts · pool")
	printStatusBody()
}

// printStatusBody renders full account/pool detail (Accounts tab + `amux status`).
func printStatusBody() {
	if !proxy.ProxyUp() {
		term.Section("proxy")
		term.KV("status", term.Badge("off", "down")+"  "+term.Dim("not running"))
		printAutoUpdate()
		term.PanelEnd()
		fmt.Println()
		profile.PrintLiveLogins()
		return
	}

	s, err := fetchProxyStatus()
	if err != nil {
		term.Error("query proxy status: %v", err)
		return
	}
	printProxyKV(s)
	printClaudeAccountsDetail(s)
	printPoolDetail(s)
	fmt.Println()
}

func printProxyKV(s *proxyStatus) {
	modeLabel := s.Mode
	if modeLabel == "" {
		modeLabel = "claude"
	}
	term.Section("proxy")
	term.KV("status", term.Badge("ok", "up")+"  "+term.Bold(proxy.ProxyAddr()))
	term.KV("mode", term.Cyan(modeLabel))
	term.KV("traffic", fmt.Sprintf("%s  %s",
		term.White(fmt.Sprintf("%d tabs", s.Sessions)),
		term.Dim(fmt.Sprintf("%d switches", s.Switches)),
	))
	if proxy.IsPublic() {
		term.KV("bind", term.Yellow("0.0.0.0:"+proxyPort())+"  "+term.Badge("info", "pub"))
		term.KV("public", term.Cyan(proxy.FormatPublicHosts()))
	}
	printAutoUpdate()
	term.PanelEnd()
}

func proxyPort() string {
	_, port, err := splitHostPort(proxy.ProxyAddr())
	if err != nil || port == "" {
		return "8787"
	}
	return port
}

func splitHostPort(addr string) (string, string, error) {
	return net.SplitHostPort(addr)
}

func printClaudeAccountsDetail(s *proxyStatus) {
	if len(s.Accounts) == 0 {
		return
	}
	term.Section("claude accounts")
	for _, a := range s.Accounts {
		state := "idle"
		serving := false
		badge := term.Badge("idle", "idle")
		if a.Active {
			state = "active"
			serving = s.Mode != "provider" && !a.Dead && !a.Disabled
		}
		if a.Dead {
			state = "dead"
			serving = false
			badge = term.Badge("err", "dead")
		} else if a.Disabled {
			state = "off"
			serving = false
			badge = term.Badge("off", "off")
		} else if a.Cooldown != "" {
			if t, e := time.Parse(time.RFC3339, a.Cooldown); e == nil && time.Now().Before(t) {
				state = "cooldown"
				serving = false
				badge = term.Badge("warn", "cool")
			}
		}
		if serving {
			badge = term.Badge("ok", "live")
		} else if a.Active && state == "active" {
			badge = term.Badge("info", "pin")
		}

		acctName := a.Account
		if acctName == "" {
			acctName = a.Profile
		}
		extra := ""
		if a.Profile != "" && a.Account != "" && a.Profile != a.Account {
			extra = "  " + term.Dim("("+a.Profile+")")
		}
		term.Row(fmt.Sprintf("%s  %s%s", badge, term.Bold(acctName), extra))

		notes := []string{}
		if a.Cooldown != "" {
			if t, e := time.Parse(time.RFC3339, a.Cooldown); e == nil {
				notes = append(notes, "cooldown "+term.Yellow(t.Local().Format("15:04")))
			}
		}
		if a.Dead {
			notes = append(notes, term.Red("re-login needed"))
		}
		if a.Disabled {
			notes = append(notes, term.Dim("am on "+a.Profile))
		}
		if a.AutoSwitches+a.ManualSwitches > 0 {
			notes = append(notes, term.Dim(fmt.Sprintf("auto %d · manual %d", a.AutoSwitches, a.ManualSwitches)))
		}
		if len(notes) > 0 {
			term.Row("      " + strings.Join(notes, "  ·  "))
		}

		if a.FiveHUsed != nil {
			printLimitBar("5h", *a.FiveHUsed, a.FiveHReset)
		}
		if a.SevenDUsed != nil {
			printLimitBar("7d", *a.SevenDUsed, a.SevenDReset)
		}
		if a.FiveHUsed == nil && a.SevenDUsed == nil && a.Remaining >= 0 {
			used := 1 - a.Remaining
			if used < 0 {
				used = 0
			}
			printLimitBar("left", used, a.LimitReset)
		}
		_ = state
	}
	term.PanelEnd()
}

func printPoolDetail(s *proxyStatus) {
	if len(s.Pool) == 0 {
		return
	}
	title := "provider pool"
	if s.Mode == "provider" {
		title = "provider pool  (active)"
	}
	term.Section(title)
	for _, p := range s.Pool {
		id, _ := p["id"].(string)
		cooling, _ := p["cooling"].(bool)
		preferred, _ := p["preferred"].(bool)
		lastUsed, _ := p["last_used"].(bool)
		prio, _ := p["priority"].(float64)

		serving := false
		badge := term.Badge("idle", "idle")
		if preferred {
			badge = term.Badge("info", "pin")
		}
		if s.Mode == "provider" && lastUsed && !cooling {
			serving = true
		} else if preferred && s.Mode == "provider" && !cooling && !anyLastUsed(s.Pool) {
			serving = true
		}
		if cooling {
			badge = term.Badge("warn", "cool")
			serving = false
		}
		if serving {
			badge = term.Badge("ok", "live")
		}

		term.Row(fmt.Sprintf("%s  %s  %s",
			badge,
			term.Dim(fmt.Sprintf("p%-2.0f", prio)),
			term.Bold(id),
		))
		if cooling {
			if cdUntil, ok := p["cooldown_until"].(string); ok && cdUntil != "" {
				if t, e := time.Parse(time.RFC3339, cdUntil); e == nil {
					term.Row("      " + term.Yellow("cooldown "+t.Local().Format("15:04")))
				}
			}
		}
	}
	term.PanelEnd()
}

func printAutoUpdate() {
	if hook.IsAutoUpdateEnabled() {
		term.KV("update", term.Green("on")+"  "+term.Dim("every 6h"))
	} else {
		term.KV("update", term.Dim("off"))
	}
}

func printLimitBar(label string, used float64, resetISO string) {
	term.Row(fmt.Sprintf("      %s  %s  %s%s",
		term.Dim(fmt.Sprintf("%-4s", label)),
		term.ProgressBar(used, 14),
		term.Bold(fmt.Sprintf("%3.0f%%", used*100)),
		term.Dim(resetSuffix(resetISO)),
	))
}

func anyLastUsed(pool []map[string]any) bool {
	for _, p := range pool {
		if last, _ := p["last_used"].(bool); last {
			return true
		}
	}
	return false
}

func resetSuffix(iso string) string {
	if iso == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return ""
	}
	loc := time.Now().Location()
	t = t.In(loc)
	now := time.Now().In(loc)
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return "  resets " + t.Format("15:04")
	}
	if t.Sub(now) < 7*24*time.Hour {
		return "  resets " + t.Format("Mon 15:04")
	}
	return "  resets " + t.Format("Jan 02 15:04")
}

// accountSummaryCounts returns live/off/cool/dead/total for dashboard KPIs.
func accountSummaryCounts(s *proxyStatus) (live, off, cool, dead, total int) {
	if s == nil {
		return
	}
	total = len(s.Accounts)
	now := time.Now()
	for _, a := range s.Accounts {
		switch {
		case a.Dead:
			dead++
		case a.Disabled:
			off++
		case a.Cooldown != "":
			if t, e := time.Parse(time.RFC3339, a.Cooldown); e == nil && now.Before(t) {
				cool++
				continue
			}
			if a.Active && s.Mode != "provider" {
				live++
			}
		case a.Active && s.Mode != "provider" && !a.Disabled && !a.Dead:
			live++
		}
	}
	return
}

func poolSummaryCounts(s *proxyStatus) (pin, live, cool, idle, total int) {
	if s == nil {
		return
	}
	total = len(s.Pool)
	for _, p := range s.Pool {
		cooling, _ := p["cooling"].(bool)
		preferred, _ := p["preferred"].(bool)
		lastUsed, _ := p["last_used"].(bool)
		if preferred {
			pin++
		}
		if cooling {
			cool++
			continue
		}
		if s.Mode == "provider" && lastUsed {
			live++
			continue
		}
		idle++
	}
	return
}
