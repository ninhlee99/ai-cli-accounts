package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
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
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
		if v := digJSON(doc, a.AccountField); v != "" {
			return v
		}
	}
	return ""
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

func cmdRm(tool, name string) {
	_ = os.Remove(bundlePath(tool, name))
	_ = os.Remove(metaPath(tool, name))
	if readActivePointer(tool) == name {
		_ = os.Remove(activePath(tool))
	}
	fmt.Printf("removed %s/%s\n", tool, name)
}
