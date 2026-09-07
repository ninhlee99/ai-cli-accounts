package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A profile bundle on disk: <base>/profiles/<tool>/<name>.amp  (encrypted tar.gz)
// plus a sidecar <name>.meta.json (cleartext metadata, no secrets).

type profileMeta struct {
	Name    string    `json:"name"`
	Tool    string    `json:"tool"`
	Account string    `json:"account"`
	Saved   time.Time `json:"saved"`
	// ID is a short handle like "claude1", assigned by position when listed —
	// not stored. Use it with `am sw claude1`.
	ID string `json:"-"`
}

type entry struct {
	Artifact Artifact `json:"artifact"`
	// Data is the file bytes, or for keychain the raw secret string bytes.
	Data []byte `json:"data"`
}

func profileDir(tool string) string { return filepath.Join(baseDir(), "profiles", tool) }
func bundlePath(tool, name string) string {
	return filepath.Join(profileDir(tool), name+".amp")
}
func metaPath(tool, name string) string {
	return filepath.Join(profileDir(tool), name+".meta.json")
}
func activePath(tool string) string {
	return filepath.Join(baseDir(), "profiles", tool, ".active")
}

func readActivePointer(tool string) string {
	b, _ := os.ReadFile(activePath(tool))
	return strings.TrimSpace(string(b))
}

func writeActivePointer(tool, name string) {
	_ = os.MkdirAll(profileDir(tool), 0o700)
	_ = os.WriteFile(activePath(tool), []byte(name), 0o600)
}

func listProfiles(tool string) []profileMeta {
	des, _ := os.ReadDir(profileDir(tool))
	var out []profileMeta
	for _, de := range des {
		if !strings.HasSuffix(de.Name(), ".meta.json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(profileDir(tool), de.Name()))
		if err != nil {
			continue
		}
		var m profileMeta
		if json.Unmarshal(b, &m) == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Saved.Equal(out[j].Saved) {
			return out[i].Saved.Before(out[j].Saved) // stable oldest-first ordering
		}
		return out[i].Name < out[j].Name
	})
	for i := range out {
		out[i].ID = fmt.Sprintf("%s%d", tool, i+1)
	}
	return out
}

// sanitizeName keeps a profile name filesystem-safe (email local+domain ok,
// drop path separators and whitespace).
func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, " ", "-")
	return s
}

func matchProfileByAccount(tool, account string) string {
	if account == "" {
		return ""
	}
	for _, p := range listProfiles(tool) {
		if p.Account == account {
			return p.Name
		}
	}
	return ""
}

// ---- capture / apply ----

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

func captureArtifact(a Artifact) (*entry, error) {
	switch a.Kind {
	case "file":
		b, err := os.ReadFile(expand(a.Path))
		if err != nil {
			if os.IsNotExist(err) && a.Optional {
				return nil, nil
			}
			return nil, fmt.Errorf("read %s: %w", a.Path, err)
		}
		return &entry{Artifact: a, Data: b}, nil
	case "keychain":
		s, err := kcGet(a.Service, a.Account)
		if err != nil {
			if a.Optional {
				return nil, nil
			}
			return nil, fmt.Errorf("keychain %s/%s: %w", a.Service, a.Account, err)
		}
		return &entry{Artifact: a, Data: []byte(s)}, nil
	default:
		return nil, fmt.Errorf("unknown artifact kind %q", a.Kind)
	}
}

func applyEntry(e entry) error {
	switch e.Artifact.Kind {
	case "file":
		p := expand(e.Artifact.Path)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		return os.WriteFile(p, e.Data, 0o600)
	case "keychain":
		return kcSet(e.Artifact.Service, e.Artifact.Account, string(e.Data))
	default:
		return fmt.Errorf("unknown artifact kind %q", e.Artifact.Kind)
	}
}

// detectAccount returns a human-readable account label (usually an email).
// An artifact's AccountField is a dotted JSON path; if it is prefixed with
// "jwt:" the value at that path is treated as a JWT and the claim after the
// next ":" is read from its payload, e.g. "jwt:tokens.id_token:email".
func detectAccount(t ToolSpec) string {
	for _, a := range t.Artifacts {
		if a.AccountField == "" {
			continue
		}
		e, err := captureArtifact(a)
		if err != nil || e == nil {
			continue
		}
		var doc map[string]any
		if json.Unmarshal(e.Data, &doc) != nil {
			continue
		}
		field := a.AccountField
		if strings.HasPrefix(field, "jwt:") {
			rest := strings.TrimPrefix(field, "jwt:")
			path, claim, ok := strings.Cut(rest, ":")
			if !ok {
				continue
			}
			if v := jwtClaim(digJSON(doc, path), claim); v != "" {
				return v
			}
			continue
		}
		if v := digJSON(doc, field); v != "" {
			return v
		}
	}
	return ""
}

func jwtClaim(tokenStr, claim string) string {
	parts := strings.Split(tokenStr, ".")
	if len(parts) < 2 {
		return ""
	}
	p := parts[1]
	if m := len(p) % 4; m != 0 {
		p += strings.Repeat("=", 4-m)
	}
	raw, err := base64.URLEncoding.DecodeString(p)
	if err != nil {
		return ""
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	return digJSON(doc, claim)
}

func digJSON(doc map[string]any, dotted string) string {
	cur := any(doc)
	for _, part := range strings.Split(dotted, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[part]
	}
	switch v := cur.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%v", v)
	default:
		return ""
	}
}

// ---- bundle serialization ----

func packEntries(entries []entry) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for i, e := range entries {
		payload, _ := json.Marshal(e)
		hdr := &tar.Header{Name: fmt.Sprintf("e%03d.json", i), Mode: 0o600, Size: int64(len(payload))}
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write(payload)
	}
	_ = tw.Close()
	_ = zw.Close()
	return buf.Bytes()
}

func unpackEntries(raw []byte) []entry {
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		die("gunzip bundle: %v", err)
	}
	tr := tar.NewReader(zr)
	var out []entry
	for {
		_, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			die("read bundle: %v", err)
		}
		b, _ := io.ReadAll(tr)
		var e entry
		if json.Unmarshal(b, &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

func cmdSave(tool, name string) {
	t := toolSpec(tool)
	if name == "" {
		name = sanitizeName(detectAccount(t))
		if name == "" {
			die("could not detect the %s account; pass a name: am save %s <name>", tool, tool)
		}
	}
	var entries []entry
	for _, a := range t.Artifacts {
		e, err := captureArtifact(a)
		if err != nil {
			die("save %s/%s: %v", tool, name, err)
		}
		if e != nil {
			entries = append(entries, *e)
		}
	}
	if len(entries) == 0 {
		die("nothing to save for %s (is it logged in?)", tool)
	}
	if err := os.MkdirAll(profileDir(tool), 0o700); err != nil {
		die("mkdir: %v", err)
	}
	enc := encrypt(packEntries(entries))
	if err := os.WriteFile(bundlePath(tool, name), enc, 0o600); err != nil {
		die("write bundle: %v", err)
	}
	m := profileMeta{Name: name, Tool: tool, Account: detectAccount(t), Saved: time.Now()}
	mb, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(metaPath(tool, name), mb, 0o600)
	writeActivePointer(tool, name)
	fmt.Printf("saved %s/%s (%s), %d artifacts\n", tool, name, orDash(m.Account), len(entries))
}

func loadProfileEntries(tool, name string) []entry {
	enc, err := os.ReadFile(bundlePath(tool, name))
	if err != nil {
		die("no profile %s/%s", tool, name)
	}
	return unpackEntries(decrypt(enc))
}

func cmdUse(tool, name string) {
	if _, err := os.Stat(bundlePath(tool, name)); err != nil {
		die("no profile %s/%s (see: am ls %s)", tool, name, tool)
	}
	// Auto-snapshot whatever is logged in now, so nothing is lost.
	if cur := detectAccount(toolSpec(tool)); cur != "" && matchProfileByAccount(tool, cur) == "" {
		fmt.Printf("current %s login (%s) is unsaved; snapshotting as '_prev'\n", tool, cur)
		cmdSave(tool, "_prev")
	}
	for _, e := range loadProfileEntries(tool, name) {
		if err := applyEntry(e); err != nil {
			die("restore %s: %v", e.Artifact.Path+e.Artifact.Service, err)
		}
	}
	writeActivePointer(tool, name)
	m := readMeta(tool, name)
	fmt.Printf("switched %s -> %s (%s)\n", tool, name, orDash(m.Account))
	if tool == "claude" {
		fmt.Println("note: a running `claude` keeps its old token until restart; use `claude --continue` to resume.")
	}
}

func readMeta(tool, name string) profileMeta {
	b, _ := os.ReadFile(metaPath(tool, name))
	var m profileMeta
	_ = json.Unmarshal(b, &m)
	return m
}

// syncActiveFromSystem reads the account the tool is currently logged in as and
// makes the matching profile active. If that live account has no profile yet,
// it is snapshotted first (named after the account). No-op if nothing is
// logged in.
func syncActiveFromSystem(tool string) {
	t := toolSpec(tool)
	acct := detectAccount(t)
	if acct == "" {
		return
	}
	name := profileNameForAccount(tool, acct)
	if name == "" {
		name = sanitizeName(acct)
		fmt.Printf("am: current %s login %q not saved yet — snapshotting it\n", tool, acct)
		cmdSave(tool, name)
		return
	}
	if readActivePointer(tool) != name {
		writeActivePointer(tool, name)
	}
}

func cmdRm(tool, name string) {
	_ = os.Remove(bundlePath(tool, name))
	_ = os.Remove(metaPath(tool, name))
	if readActivePointer(tool) == name {
		_ = os.Remove(activePath(tool))
	}
	fmt.Printf("removed %s/%s\n", tool, name)
}
