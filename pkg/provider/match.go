package provider

import (
	"fmt"
	"os"
	"strings"
)

// MatchID resolves a pool provider id. Accepts a full id (`gemini:web:01`)
// or a unique prefix (`gemini:web`, `chatgpt`). Ambiguous prefixes error.
func MatchID(path, q string) (string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return "", fmt.Errorf("empty account id")
	}
	ids, err := listedProviderIDs(path)
	if err != nil {
		return "", err
	}
	ql := strings.ToLower(q)
	for _, id := range ids {
		if strings.EqualFold(id, q) {
			return id, nil
		}
	}
	var hits []string
	for _, id := range ids {
		idl := strings.ToLower(id)
		if idl == ql || strings.HasPrefix(idl, ql+":") {
			hits = append(hits, id)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return "", fmt.Errorf("no provider with id %q (see: am accounts)", q)
	default:
		return "", fmt.Errorf("ambiguous %q — %s", q, strings.Join(hits, ", "))
	}
}

func listedProviderIDs(path string) ([]string, error) {
	f, err := LoadConfigFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var rows []ProviderConfig
	if f != nil {
		rows = append(rows, f.Providers...)
	}
	seen := map[string]bool{}
	var ids []string
	for _, p := range rows {
		if p.ID == "" || seen[p.ID] {
			continue
		}
		seen[p.ID] = true
		ids = append(ids, p.ID)
	}
	if row, ok := CodexAutoRow(rows); ok && !seen[row.ID] {
		ids = append(ids, row.ID)
	}
	return ids, nil
}
