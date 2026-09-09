package types

import (
	"fmt"
	"regexp"
	"strconv"
)

// idPattern matches the unified account-ID format used by both the Profile
// system (am add/ls/sw) and the Pool system (am login/accounts/api):
// "<prefix>:<NN>", e.g. "claudecli:01", "chatgptweb:02". The number is
// always zero-padded to at least 2 digits.
var idPattern = regexp.MustCompile(`^([a-z0-9]+):([0-9]{2,})$`)

// FormatID renders a prefix and a 1-based index into the unified ID format,
// zero-padding the number to at least 2 digits ("claudecli", 1 -> "claudecli:01").
func FormatID(prefix string, n int) string {
	return fmt.Sprintf("%s:%02d", prefix, n)
}

// ParseID splits a unified-format ID back into its prefix and number. ok is
// false if id doesn't match the "<prefix>:<NN>" shape (e.g. a legacy ID or a
// user-chosen custom name from `am api add`).
func ParseID(id string) (prefix string, n int, ok bool) {
	m := idPattern.FindStringSubmatch(id)
	if m == nil {
		return "", 0, false
	}
	num, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0, false
	}
	return m[1], num, true
}
