package auth

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"

	"ai-cli-accounts/pkg/types"
)

// KCGet retrieves a password item from macOS Keychain.
func KCGet(service, account string) (string, error) {
	args := []string{"find-generic-password", "-s", service, "-w"}
	if account != "" {
		args = append(args, "-a", account)
	}
	cmd := exec.Command("security", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("security: %s", strings.TrimSpace(errb.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// KCAccount reads the existing item's account attribute, if any.
func KCAccount(service string) string {
	cmd := exec.Command("security", "find-generic-password", "-s", service)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(out), "\n") {
		if strings.Contains(ln, `"acct"`) {
			if i := strings.Index(ln, `="`); i >= 0 {
				return strings.TrimSuffix(ln[i+2:], `"`)
			}
		}
	}
	return ""
}

// KCSet writes or updates a generic-password item in macOS Keychain.
func KCSet(service, account, secret string) error {
	if account == "" {
		if a := KCAccount(service); a != "" {
			account = a
		} else {
			account = types.CurrentUser()
		}
	}
	// -X takes the password as a hex string, sidestepping argv quoting issues.
	args := []string{
		"add-generic-password", "-U",
		"-s", service, "-a", account,
		"-X", hex.EncodeToString([]byte(secret)),
	}
	cmd := exec.Command("security", args...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("security add: %s", strings.TrimSpace(errb.String()))
	}
	return nil
}
