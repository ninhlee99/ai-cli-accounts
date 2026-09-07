package main

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// feedbackRepo is where `am feedback` files issues. Change if you fork.
const feedbackRepo = "ninhlee99/ai-cli-accounts"

// cmdFeedback opens a prefilled GitHub issue for a bug report or idea.
// Usage: am feedback [-b|--bug | -i|--idea] [title text...]
//   - with no title, prompts for one (and a body) interactively
//   - the body is pre-filled with OS/arch + any hook/proxy state, so bug
//     reports carry useful context without the user typing it
func cmdFeedback(args []string) {
	kind := "bug"
	var titleWords []string
	for _, a := range args {
		switch a {
		case "-b", "--bug":
			kind = "bug"
		case "-i", "--idea":
			kind = "idea"
		default:
			titleWords = append(titleWords, a)
		}
	}
	title := strings.Join(titleWords, " ")

	if title == "" {
		fmt.Print("short title for the issue: ")
		title = readLine()
		if title == "" {
			die("cancelled (no title given)")
		}
	}

	fmt.Println("describe what happened / what you'd like — blank line to finish:")
	body := readMultiline()

	full := feedbackBody(body)
	label := "bug"
	if kind == "idea" {
		label = "enhancement"
	}

	if openIssueViaGH(title, full, label) {
		return
	}
	openIssueInBrowser(title, full)
}

func feedbackBody(userText string) string {
	var b strings.Builder
	if userText != "" {
		b.WriteString(userText)
		b.WriteString("\n\n")
	}
	b.WriteString("---\n")
	fmt.Fprintf(&b, "OS: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if active := readActivePointer("claude"); active != "" {
		fmt.Fprintf(&b, "active claude profile: %s\n", active)
	}
	return b.String()
}

// openIssueViaGH shells out to `gh issue create` when the CLI is available
// and authenticated. Returns false if it couldn't be used, so the caller
// falls back to a browser URL.
func openIssueViaGH(title, body, label string) bool {
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return false
	}
	args := []string{"issue", "create", "-R", feedbackRepo, "-t", title, "-b", body}
	if label != "" {
		args = append(args, "-l", label)
	}
	cmd := exec.Command(ghPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "am: gh issue create failed (%v) — opening browser instead\n", err)
		return false
	}
	return true
}

// openIssueInBrowser builds a prefilled "new issue" URL and opens it, for
// machines without `gh` (or not logged in).
func openIssueInBrowser(title, body string) {
	u := fmt.Sprintf("https://github.com/%s/issues/new?title=%s&body=%s",
		feedbackRepo, url.QueryEscape(title), url.QueryEscape(body))
	fmt.Println("opening:", u)
	var openCmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		openCmd = exec.Command("open", u)
	case "linux":
		openCmd = exec.Command("xdg-open", u)
	default:
		fmt.Println("open that URL in a browser to file the issue.")
		return
	}
	if err := openCmd.Start(); err != nil {
		fmt.Println("couldn't launch a browser — open the URL above manually.")
	}
}

func readLine() string {
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line)
}

// readMultiline reads lines until a blank one (or EOF).
func readMultiline() string {
	sc := bufio.NewScanner(os.Stdin)
	var lines []string
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			break
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

