package profile

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"ai-cli-accounts/pkg/auth"
	"ai-cli-accounts/pkg/types"
)

func ProfileDir(tool string) string {
	return filepath.Join(types.BaseDir(), "profiles", tool)
}

func BundlePath(tool, name string) string {
	return filepath.Join(ProfileDir(tool), name+".amp")
}

func MetaPath(tool, name string) string {
	return filepath.Join(ProfileDir(tool), name+".meta.json")
}

func ActivePath(tool string) string {
	return filepath.Join(types.BaseDir(), "profiles", tool, ".active")
}

func ReadActivePointer(tool string) string {
	b, _ := os.ReadFile(ActivePath(tool))
	return strings.TrimSpace(string(b))
}

func WriteActivePointer(tool, name string) {
	_ = os.MkdirAll(ProfileDir(tool), 0o700)
	_ = os.WriteFile(ActivePath(tool), []byte(name), 0o600)
}

func ListProfiles(tool string) []types.ProfileMeta {
	des, _ := os.ReadDir(ProfileDir(tool))
	var out []types.ProfileMeta
	for _, de := range des {
		if !strings.HasSuffix(de.Name(), ".meta.json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(ProfileDir(tool), de.Name()))
		if err != nil {
			continue
		}
		var m types.ProfileMeta
		if json.Unmarshal(b, &m) == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Saved.Equal(out[j].Saved) {
			return out[i].Saved.Before(out[j].Saved)
		}
		return out[i].Name < out[j].Name
	})
	for i := range out {
		out[i].ID = fmt.Sprintf("%s%d", tool, i+1)
	}
	return out
}

func SanitizeName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, " ", "-")
	return s
}

func MatchProfileByAccount(tool, account string) string {
	if account == "" {
		return ""
	}
	for _, p := range ListProfiles(tool) {
		if strings.EqualFold(p.Account, account) {
			return p.Name
		}
	}
	return ""
}

func ProfileNameForAccount(tool, account string) string {
	return MatchProfileByAccount(tool, account)
}

func ConfigPath() string { return filepath.Join(types.BaseDir(), "config.json") }

func LoadConfig() types.ToolConfig {
	b, err := os.ReadFile(ConfigPath())
	if err != nil {
		c := types.DefaultToolConfig()
		SaveConfig(c)
		return c
	}
	var c types.ToolConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return types.DefaultToolConfig()
	}
	def := types.DefaultToolConfig()
	for k, v := range def.Tools {
		if _, ok := c.Tools[k]; !ok {
			c.Tools[k] = v
		}
	}
	return c
}

func SaveConfig(c types.ToolConfig) {
	_ = os.MkdirAll(types.BaseDir(), 0o700)
	b, _ := json.MarshalIndent(c, "", "  ")
	_ = os.WriteFile(ConfigPath(), b, 0o600)
}

func ToolNames(c types.ToolConfig) []string {
	var n []string
	for k := range c.Tools {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

func LookupToolSpec(tool string) (types.ToolSpec, bool) {
	c := LoadConfig()
	spec, ok := c.Tools[tool]
	return spec, ok
}

func ToolSpec(tool string) types.ToolSpec {
	c := LoadConfig()
	spec, ok := c.Tools[tool]
	if !ok {
		panic(fmt.Sprintf("unknown tool %q (known: %s)", tool, strings.Join(ToolNames(c), ", ")))
	}
	return spec
}

// DetectAccount reads the logged-in email from configured files or keychain.
func DetectAccount(spec types.ToolSpec) string {
	for _, a := range spec.Artifacts {
		if a.AccountField == "" {
			continue
		}
		b, err := os.ReadFile(a.Path)
		if err != nil {
			continue
		}
		if strings.HasPrefix(a.AccountField, "jwt:") {
			parts := strings.Split(a.AccountField[4:], ":")
			tokStr := jsonDottedPath(b, parts[0])
			if tokStr != "" {
				if email := parseJWTEmail(tokStr, parts[1]); email != "" {
					return email
				}
			}
			continue
		}
		if v := jsonDottedPath(b, a.AccountField); v != "" {
			return v
		}
	}
	return ""
}

func jsonDottedPath(b []byte, path string) string {
	var cur any
	if err := json.Unmarshal(b, &cur); err != nil {
		return ""
	}
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[k]
	}
	if s, ok := cur.(string); ok {
		return s
	}
	return ""
}

func parseJWTEmail(tok, field string) string {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return ""
	}
	seg := parts[1]
	if m := len(seg) % 4; m != 0 {
		seg += strings.Repeat("=", 4-m)
	}
	payload, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		return ""
	}
	return jsonDottedPath(payload, field)
}

func SnapshotArtifact(a types.Artifact) (types.ProfileEntry, error) {
	switch a.Kind {
	case "file":
		b, err := os.ReadFile(a.Path)
		if err != nil {
			if a.Optional && os.IsNotExist(err) {
				return types.ProfileEntry{Artifact: a}, nil
			}
			return types.ProfileEntry{}, fmt.Errorf("read %s: %w", a.Path, err)
		}
		return types.ProfileEntry{Artifact: a, Data: b}, nil
	case "keychain":
		s, err := auth.KCGet(a.Service, a.Account)
		if err != nil {
			if a.Optional {
				return types.ProfileEntry{Artifact: a}, nil
			}
			return types.ProfileEntry{}, fmt.Errorf("keychain %s/%s: %w", a.Service, a.Account, err)
		}
		return types.ProfileEntry{Artifact: a, Data: []byte(s)}, nil
	default:
		return types.ProfileEntry{}, fmt.Errorf("unknown artifact kind: %s", a.Kind)
	}
}

func ApplyEntry(e types.ProfileEntry) error {
	if len(e.Data) == 0 && e.Artifact.Optional {
		return nil
	}
	switch e.Artifact.Kind {
	case "file":
		if err := os.MkdirAll(filepath.Dir(e.Artifact.Path), 0o755); err != nil {
			return fmt.Errorf("mkdir: %w", err)
		}
		return os.WriteFile(e.Artifact.Path, e.Data, 0o600)
	case "keychain":
		return auth.KCSet(e.Artifact.Service, e.Artifact.Account, string(e.Data))
	default:
		return fmt.Errorf("unknown kind: %s", e.Artifact.Kind)
	}
}

func LoadProfileEntries(tool, name string) []types.ProfileEntry {
	p := BundlePath(tool, name)
	enc, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	plain, err := auth.Decrypt(enc)
	if err != nil {
		return nil
	}
	return unpackEntries(plain)
}

func unpackEntries(plain []byte) []types.ProfileEntry {
	gr, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		return nil
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	var out []types.ProfileEntry
	for {
		_, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil
		}
		var e types.ProfileEntry
		if json.Unmarshal(b, &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

func WriteBundle(tool, name string, entries []types.ProfileEntry) error {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for i, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			return err
		}
		hdr := &tar.Header{
			Name:    fmt.Sprintf("e%03d.json", i),
			Mode:    0o600,
			Size:    int64(len(b)),
			ModTime: time.Now(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(b); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gw.Close(); err != nil {
		return err
	}

	enc, err := auth.Encrypt(buf.Bytes())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(ProfileDir(tool), 0o700); err != nil {
		return err
	}
	return os.WriteFile(BundlePath(tool, name), enc, 0o600)
}

func UpdateProfileEntry(tool, name string, updated types.ProfileEntry) error {
	entries := LoadProfileEntries(tool, name)
	for i, e := range entries {
		if e.Artifact.Kind == updated.Artifact.Kind &&
			e.Artifact.Service == updated.Artifact.Service &&
			e.Artifact.Path == updated.Artifact.Path {
			entries[i] = updated
			return WriteBundle(tool, name, entries)
		}
	}
	entries = append(entries, updated)
	return WriteBundle(tool, name, entries)
}

func LoadClaudeToken(tool, name string) *types.Token {
	for _, e := range LoadProfileEntries(tool, name) {
		if e.Artifact.Kind != "keychain" || e.Artifact.Service != auth.ClaudeKeychainService {
			continue
		}
		if t := auth.ParseClaudeCreds(e.Data); t != nil {
			return t
		}
	}
	return &types.Token{Remaining: -1}
}

func InstallActiveProfile(name string) bool {
	entries := LoadProfileEntries("claude", name)
	ok := true
	for i, e := range entries {
		if e.Artifact.Kind != "keychain" || e.Artifact.Service != auth.ClaudeKeychainService {
			continue
		}
		var c auth.ClaudeCreds
		if json.Unmarshal(e.Data, &c) != nil {
			break
		}
		if auth.TokenExpiryNeedsRefresh(c.ClaudeAiOauth.ExpiresAt) {
			if c.ClaudeAiOauth.RefreshToken == "" {
				log.Printf("am: token for claude/%s is expired and has no refresh token", name)
				ok = false
				break
			}
			rr, err := auth.RefreshClaudeToken(c.ClaudeAiOauth.RefreshToken)
			if err != nil {
				log.Printf("am: refresh token for claude/%s failed: %v", name, err)
				ok = false
				break
			}
			newData, _, err := auth.RefreshedCredsJSON(e.Data, rr, c.ClaudeAiOauth.RefreshToken)
			if err != nil {
				log.Printf("am: rebuild refreshed creds for claude/%s failed: %v", name, err)
				ok = false
				break
			}
			entries[i].Data = newData
			if err := UpdateProfileEntry("claude", name, entries[i]); err != nil {
				log.Printf("am: could not persist refreshed token into bundle claude/%s: %v", name, err)
			}
		}
		break
	}
	if !ok {
		return false
	}
	for _, e := range entries {
		_ = ApplyEntry(e)
	}
	return true
}

func ReadMeta(tool, name string) types.ProfileMeta {
	b, _ := os.ReadFile(MetaPath(tool, name))
	var m types.ProfileMeta
	_ = json.Unmarshal(b, &m)
	return m
}

func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func CmdSave(tool, name string) (string, error) {
	spec, ok := LookupToolSpec(tool)
	if !ok {
		return "", fmt.Errorf("unknown tool %q", tool)
	}
	acct := DetectAccount(spec)
	if name == "" {
		name = SanitizeName(acct)
		if name == "" {
			return "", fmt.Errorf("could not detect the %s account; pass a name: am add %s <name>", tool, tool)
		}
	} else {
		name = SanitizeName(name)
	}

	var entries []types.ProfileEntry
	for _, a := range spec.Artifacts {
		e, err := SnapshotArtifact(a)
		if err != nil {
			if !a.Optional {
				return "", fmt.Errorf("save %s/%s: %w", tool, name, err)
			}
			continue
		}
		if len(e.Data) > 0 || !a.Optional {
			entries = append(entries, e)
		}
	}

	if len(entries) == 0 {
		return "", fmt.Errorf("nothing to save for %s (is it logged in?)", tool)
	}

	if err := WriteBundle(tool, name, entries); err != nil {
		return "", fmt.Errorf("write bundle: %w", err)
	}

	meta := types.ProfileMeta{Name: name, Tool: tool, Account: acct, Saved: time.Now()}
	mb, _ := json.MarshalIndent(meta, "", "  ")
	if err := WriteFileAtomic(MetaPath(tool, name), mb, 0o600); err != nil {
		return "", fmt.Errorf("write meta: %w", err)
	}

	WriteActivePointer(tool, name)
	acctDisplay := acct
	if acctDisplay == "" {
		acctDisplay = "-"
	}
	fmt.Printf("saved %s/%s (%s), %d artifacts\n", tool, name, acctDisplay, len(entries))
	AutoBackup()
	return name, nil
}

func SaveActiveProfile(tool, name string) (string, error) {
	return CmdSave(tool, name)
}

func CmdUse(tool, name string) error {
	if _, err := os.Stat(BundlePath(tool, name)); err != nil {
		return fmt.Errorf("no profile %s/%s (see: am ls %s)", tool, name, tool)
	}
	// Always resave whatever is logged in now into its own profile first, so
	// a token Claude Code rotated while that account was active (refresh
	// tokens are single-use / rotate-on-use) isn't lost the moment we
	// overwrite the keychain with a different account.
	if spec, ok := LookupToolSpec(tool); ok {
		if cur := DetectAccount(spec); cur != "" {
			if matched := MatchProfileByAccount(tool, cur); matched != "" {
				_, _ = CmdSave(tool, matched)
			} else {
				fmt.Printf("current %s login (%s) is unsaved; snapshotting as '_prev'\n", tool, cur)
				_, _ = CmdSave(tool, "_prev")
			}
		}
	}
	if tool == "claude" {
		// Route through InstallActiveProfile rather than applying entries
		// raw: it refreshes the bundled access token first when it's
		// expired/near-expiry (using the refresh token captured at
		// save/export time), persists the refreshed creds back into the
		// bundle, and only then installs into the keychain. Without this, a
		// profile imported via `am import --activate claude=<name>` (or any
		// `am use claude <name>`/`am sw` on a token that's been sitting
		// unused) would install a stale access token verbatim and fail on
		// first use even though a perfectly good refresh token was right
		// there — mirrors what Rotator.ForceSwitch already does for the
		// proxy's own `/_am/switch` path.
		if !InstallActiveProfile(name) {
			return fmt.Errorf("refresh token for %q is dead — log into it again before switching to it", name)
		}
	} else {
		for _, e := range LoadProfileEntries(tool, name) {
			if err := ApplyEntry(e); err != nil {
				return fmt.Errorf("restore %s: %w", e.Artifact.Path+e.Artifact.Service, err)
			}
		}
	}
	WriteActivePointer(tool, name)
	meta := ReadMeta(tool, name)
	acctDisplay := meta.Account
	if acctDisplay == "" {
		acctDisplay = "-"
	}
	fmt.Printf("switched %s -> %s (%s)\n", tool, name, acctDisplay)
	if tool == "claude" {
		fmt.Println("note: a running `claude` keeps its old token until restart; use `claude --continue` to resume.")
	}
	return nil
}

func SyncActiveFromSystem(tool string) {
	spec, ok := LookupToolSpec(tool)
	if !ok {
		return
	}
	acct := DetectAccount(spec)
	if acct == "" {
		return
	}
	name := ProfileNameForAccount(tool, acct)
	if name == "" {
		name = SanitizeName(acct)
		fmt.Printf("am: current %s login %q not saved yet — snapshotting it\n", tool, acct)
		_, _ = CmdSave(tool, name)
		return
	}
	if ReadActivePointer(tool) != name {
		WriteActivePointer(tool, name)
	}
}

func PrintLiveLogins() {
	c := LoadConfig()
	tools := ToolNames(c)
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "TOOL\tLOGGED IN AS\tSAVED PROFILE")
	for _, tn := range tools {
		acct := DetectAccount(ToolSpec(tn))
		acctDisplay := acct
		if acctDisplay == "" {
			acctDisplay = "-"
		}
		prof := MatchProfileByAccount(tn, acct)
		if prof == "" {
			prof = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", tn, acctDisplay, prof)
	}
	w.Flush()
}
