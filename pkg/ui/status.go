package ui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"amux-accounts/pkg/hook"
	"amux-accounts/pkg/profile"
	"amux-accounts/pkg/proxy"
)

const (
	dotGreen = "\x1b[32m●\x1b[0m"
	dotGray  = "\x1b[90m○\x1b[0m"
)

// CmdStatus displays the rich status table for proxy, Claude accounts, and provider pool.
func CmdStatus() {
	if !proxy.ProxyUp() {
		fmt.Printf("proxy:       %s not running (starts automatically when you launch claude, stays up until `amux proxy down --force`)\n", dotGray)
		fmt.Printf("base URL:    https://api.anthropic.com  (direct — no rotation, no auto-switch on rate limit)\n")
		if hook.IsAutoUpdateEnabled() {
			fmt.Printf("auto-update: %s enabled (LaunchAgent checks every 6h)\n\n", dotGreen)
		} else {
			fmt.Printf("auto-update: %s disabled (run `amux setup --auto-update` to enable)\n\n", dotGray)
		}
		profile.PrintLiveLogins()
		return
	}

	resp, err := http.Get(proxy.ProxyBase() + "/_am/status")
	if err != nil {
		fmt.Printf("error querying proxy status: %v\n", err)
		return
	}
	defer resp.Body.Close()

	var s struct {
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
			FiveHUsed      *float64 `json:"5h_used"`
			FiveHReset     string   `json:"5h_reset"`
			SevenDUsed     *float64 `json:"7d_used"`
			SevenDReset    string   `json:"7d_reset"`
			AutoSwitches   int      `json:"auto_switches"`
			ManualSwitches int      `json:"manual_switches"`
		} `json:"accounts"`
		Pool []map[string]any `json:"pool"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		fmt.Printf("status decode error: %v\n", err)
		return
	}

	addr := os.Getenv("AM_PROXY_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8787"
	}

	fmt.Printf("proxy:       %s running on %s · %d claude tab(s) attached · %d switch(es) this run\n", dotGreen, addr, s.Sessions, s.Switches)
	fmt.Printf("base URL:    %s  (rotating through %d account(s) below)\n", proxy.ProxyBase(), len(s.Accounts))
	if hook.IsAutoUpdateEnabled() {
		fmt.Printf("auto-update: %s enabled (LaunchAgent checks every 6h)\n\n", dotGreen)
	} else {
		fmt.Printf("auto-update: %s disabled (run `amux setup --auto-update` to enable)\n\n", dotGray)
	}

	for _, a := range s.Accounts {
		mark := "  "
		state := "idle"
		if a.Active {
			mark = "> "
			state = "active"
		}
		if a.Dead {
			state = "dead"
		} else if a.Cooldown != "" {
			if t, e := time.Parse(time.RFC3339, a.Cooldown); e == nil && time.Now().Before(t) {
				state = "cooldown"
			}
		}
		acctName := a.Account
		if acctName == "" {
			acctName = a.Profile
		}
		line := fmt.Sprintf("%s%-8s  %s", mark, state, acctName)
		if a.Cooldown != "" {
			if t, e := time.Parse(time.RFC3339, a.Cooldown); e == nil {
				line += "   (cooldown until " + t.Local().Format("15:04") + ")"
			}
		}
		if a.Dead {
			line += "   (refresh dead — re-login to restore)"
		}
		fmt.Println(line)
		if a.FiveHUsed != nil {
			fmt.Printf("           5h limit:  %.0f%% used%s\n", *a.FiveHUsed*100, resetSuffix(a.FiveHReset))
		}
		if a.SevenDUsed != nil {
			fmt.Printf("           7d limit:  %.0f%% used%s\n", *a.SevenDUsed*100, resetSuffix(a.SevenDReset))
		}
		if a.FiveHUsed == nil && a.SevenDUsed == nil && a.Remaining >= 0 {
			line := fmt.Sprintf("           %.0f%% left", a.Remaining*100)
			if a.LimitReset != "" {
				if t, e := time.Parse(time.RFC3339, a.LimitReset); e == nil {
					line += "   resets " + t.Local().Format("15:04")
				}
			}
			fmt.Println(line)
		}
	}

	if len(s.Pool) > 0 {
		modeNote := ""
		if s.Mode == "provider" {
			modeNote = "  (currently handling Claude Code traffic — see base URL above)"
		}
		fmt.Printf("\nmulti-provider pool (am accounts / am sw <provider>)%s\n", modeNote)
		for _, p := range s.Pool {
			id, _ := p["id"].(string)
			cooling, _ := p["cooling"].(bool)
			preferred, _ := p["preferred"].(bool)
			prio, _ := p["priority"].(float64)

			mark := "  "
			state := "idle"
			if preferred {
				mark = "> "
				state = "active"
			}
			if cooling {
				state = "cooldown"
			}

			line := fmt.Sprintf("%s%-8s  prio %-2.0f  %s", mark, state, prio, id)
			if cooling {
				if cdUntil, ok := p["cooldown_until"].(string); ok && cdUntil != "" {
					if t, e := time.Parse(time.RFC3339, cdUntil); e == nil {
						line += "   (cooldown until " + t.Local().Format("15:04") + ")"
					}
				}
			}
			fmt.Println(line)
		}
	}
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
		return "   resets " + t.Format("15:04")
	}
	if t.Sub(now) < 7*24*time.Hour {
		return "   resets " + t.Format("Mon 15:04")
	}
	return "   resets " + t.Format("Jan 02 15:04")
}
