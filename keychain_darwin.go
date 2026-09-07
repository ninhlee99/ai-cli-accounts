package main

// macOS Keychain access via the `security` CLI. We deliberately avoid a library
// here: Claude Code stores its credential as a raw JSON string, and libraries
// like go-keyring wrap the value (base64 + prefix), which would corrupt it.

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
)

func kcGet(service, account string) (string, error) {
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

// kcAccount reads the existing item's account attribute, if any.
func kcAccount(service string) string {
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

func kcSet(service, account, secret string) error {
	if account == "" {
		if a := kcAccount(service); a != "" {
			account = a
		} else {
			account = currentUser()
		}
	}
	// -X takes the password as a hex string, sidestepping any argv quoting
	// problems with JSON braces / quotes / newlines in the secret.
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
