package profile

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"amux-accounts/pkg/auth"
	"amux-accounts/pkg/types"
	"golang.org/x/crypto/scrypt"
	"golang.org/x/term"
)

const ExportMagic = "AMEXP1."

type PortableProfile struct {
	Tool    string               `json:"tool"`
	Name    string               `json:"name"`
	Account string               `json:"account"`
	Saved   time.Time            `json:"saved"`
	Entries []types.ProfileEntry `json:"entries"`
}

type PortableBundle struct {
	Version  int               `json:"v"`
	Exported time.Time         `json:"exported"`
	Profiles []PortableProfile `json:"profiles"`
}

func DeriveKey(pass, salt []byte) ([]byte, error) {
	return scrypt.Key(pass, salt, 1<<15, 8, 1, 32)
}

func CollectBundle(wantTool string, wantNames map[string]bool) PortableBundle {
	var b PortableBundle
	b.Version = 1
	b.Exported = time.Now()
	for _, tn := range ToolNames(LoadConfig()) {
		if wantTool != "" && tn != wantTool {
			continue
		}
		for _, m := range ListProfiles(tn) {
			if strings.HasPrefix(m.Name, "_") {
				continue
			}
			if len(wantNames) > 0 && !wantNames[m.Name] {
				continue
			}
			b.Profiles = append(b.Profiles, PortableProfile{
				Tool:    tn,
				Name:    m.Name,
				Account: m.Account,
				Saved:   m.Saved,
				Entries: LoadProfileEntries(tn, m.Name),
			})
		}
	}
	return b
}

// SealBundle gzips + AES-256-GCM encrypts the bundle. When saltInline is true
// the key is passphrase-derived and a fresh salt is prepended; otherwise the
// caller's key is used as-is (16 zero bytes stand in for the salt slot).
func SealBundle(b PortableBundle, key []byte, saltInline bool) (string, error) {
	plain, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(plain); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}

	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if saltInline {
		if _, err := io.ReadFull(rand.Reader, salt); err != nil {
			return "", err
		}
		derived, err := DeriveKey(key, salt)
		if err != nil {
			return "", err
		}
		key = derived
	}
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ct := gcm.Seal(nil, nonce, gz.Bytes(), nil)
	out := append(append(salt, nonce...), ct...)
	return ExportMagic + base64.URLEncoding.EncodeToString(out), nil
}

// OpenBundle decrypts a sealed blob. When saltInline the key is the raw
// passphrase and the salt is read from the blob; otherwise key is used directly.
func OpenBundle(blob string, key []byte, saltInline bool) (*PortableBundle, error) {
	blob = strings.TrimSpace(blob)
	if !strings.HasPrefix(blob, ExportMagic) {
		return nil, fmt.Errorf("invalid export format: missing magic prefix")
	}
	clean := strings.TrimPrefix(blob, ExportMagic)
	raw, err := base64.URLEncoding.DecodeString(clean)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(clean)
		if err != nil {
			return nil, fmt.Errorf("decode export payload: %w", err)
		}
	}
	if len(raw) < 16+12+16 {
		return nil, fmt.Errorf("export payload too short")
	}

	salt, nonce, ct := raw[:16], raw[16:28], raw[28:]
	if saltInline {
		derived, err := DeriveKey(key, salt)
		if err != nil {
			return nil, err
		}
		key = derived
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	gz, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt failed (wrong passphrase or key?)")
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("gunzip: %w", err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	var b PortableBundle
	if err := json.Unmarshal(plain, &b); err != nil {
		return nil, fmt.Errorf("bad bundle json: %w", err)
	}
	return &b, nil
}

func BackupDir() string { return filepath.Join(types.BaseDir(), "backups") }
func TrashDir(tool string) string { return filepath.Join(types.BaseDir(), "trash", tool) }

// AutoBackup writes an encrypted snapshot of every profile across all tools to
// ~/.am/backups/, keyed by the machine's master key. Keeps the 20 most recent.
func AutoBackup() {
	b := CollectBundle("", nil)
	if len(b.Profiles) == 0 {
		return
	}
	dir := BackupDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	blob, err := SealBundle(b, auth.MasterKey(), false)
	if err != nil {
		return
	}
	name := time.Now().Format("2006-01-02_150405") + ".amexp"
	_ = os.WriteFile(filepath.Join(dir, name), []byte(blob+"\n"), 0o600)
	PruneBackups(dir, 20)
}

func PruneBackups(dir string, keep int) {
	des, _ := os.ReadDir(dir)
	var files []string
	for _, de := range des {
		if strings.HasSuffix(de.Name(), ".amexp") {
			files = append(files, de.Name())
		}
	}
	sort.Strings(files)
	for i := 0; i < len(files)-keep; i++ {
		_ = os.Remove(filepath.Join(dir, files[i]))
	}
}

// CmdRestoreBackup re-imports the most recent auto-backup (or one named by substring).
func CmdRestoreBackup(which string) (int, error) {
	dir := BackupDir()
	des, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("no auto-backups in %s", dir)
	}
	var files []string
	for _, de := range des {
		if strings.HasSuffix(de.Name(), ".amexp") && (which == "" || strings.Contains(de.Name(), which)) {
			files = append(files, de.Name())
		}
	}
	if len(files) == 0 {
		return 0, fmt.Errorf("no auto-backups in %s", dir)
	}
	sort.Strings(files)
	pick := files[len(files)-1]
	blob, err := os.ReadFile(filepath.Join(dir, pick))
	if err != nil {
		return 0, fmt.Errorf("read backup: %w", err)
	}
	b, err := OpenBundle(strings.TrimSpace(string(blob)), auth.MasterKey(), false)
	if err != nil {
		return 0, err
	}
	n := MergeBundle(b)
	fmt.Printf("restored from %s: %d profile(s) added\n", pick, n)
	return n, nil
}

// DefaultExportPath names the file `am export` writes when -o isn't given.
func DefaultExportPath(wantTool string, rest []string) string {
	stamp := time.Now().Format("20060102-150405")
	base := "all-accounts"
	if wantTool != "" {
		base = wantTool
		for _, n := range rest {
			base += "-" + SanitizeName(n)
		}
	}
	return fmt.Sprintf("%s-%s.amexp", base, stamp)
}

func ReadPassphrase(prompt string) ([]byte, error) {
	fmt.Fprint(os.Stderr, prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return nil, fmt.Errorf("read passphrase: %w", err)
		}
		return b, nil
	}
	if v := os.Getenv("AM_PASSPHRASE"); v != "" {
		fmt.Fprintln(os.Stderr, "(from $AM_PASSPHRASE)")
		return []byte(v), nil
	}
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	return []byte(strings.TrimRight(line, "\r\n")), nil
}

func CmdExport(args []string) error {
	var wantTool, outPath string
	toStdout := false
	wantNames := map[string]bool{}
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-o", "--output":
			i++
			if i >= len(args) {
				return fmt.Errorf("usage: am export ... -o <file>")
			}
			outPath = args[i]
		case "--stdout":
			toStdout = true
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) > 0 {
		wantTool = rest[0]
		for _, n := range rest[1:] {
			wantNames[n] = true
		}
	}

	bundle := CollectBundle(wantTool, wantNames)
	if len(bundle.Profiles) == 0 {
		return fmt.Errorf("no profiles to export")
	}

	pass, err := ReadPassphrase("passphrase to encrypt the export: ")
	if err != nil {
		return err
	}
	pass2, err := ReadPassphrase("repeat passphrase: ")
	if err != nil {
		return err
	}
	if !bytes.Equal(pass, pass2) {
		return fmt.Errorf("passphrases do not match")
	}

	blob, err := SealBundle(bundle, pass, true)
	if err != nil {
		return err
	}

	if toStdout {
		fmt.Println(blob)
		fmt.Fprintf(os.Stderr, "\nexported %d profile(s). Copy the line above to the other machine and run: am import\n", len(bundle.Profiles))
		return nil
	}

	if outPath == "" {
		outPath = DefaultExportPath(wantTool, rest[1:])
	}
	if err := os.WriteFile(outPath, []byte(blob+"\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	fmt.Fprintf(os.Stderr, "exported %d profile(s) to %s. Copy that file to the other machine and run: am import -f %s\n", len(bundle.Profiles), outPath, filepath.Base(outPath))
	return nil
}

func CmdImport(args []string) error {
	activate := map[string]string{}
	var src io.Reader = os.Stdin
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--activate":
			i++
			if i < len(args) {
				t, n, _ := strings.Cut(args[i], "=")
				activate[t] = n
			}
		case "--file", "-f":
			i++
			if i < len(args) {
				f, err := os.Open(args[i])
				if err != nil {
					return fmt.Errorf("open %s: %w", args[i], err)
				}
				defer f.Close()
				src = f
			}
		default:
			return fmt.Errorf("unknown flag %q", args[i])
		}
	}

	blob, err := ReadExportBlob(src)
	if err != nil {
		return err
	}
	pass, err := ReadPassphrase("passphrase to decrypt the import: ")
	if err != nil {
		return err
	}
	bundle, err := OpenBundle(blob, pass, true)
	if err != nil {
		return err
	}

	added := MergeBundle(bundle)
	kept := len(bundle.Profiles) - added
	fmt.Printf("\nimport done: %d added, %d already present (kept).\n", added, kept)

	for tool, name := range activate {
		fmt.Printf("activating %s -> %s\n", tool, name)
		_ = CmdUse(tool, name)
	}
	if len(activate) == 0 {
		fmt.Println("nothing was applied to the system; run `am sw <id>` to switch to one.")
	}
	return nil
}

func ReadExportBlob(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, ExportMagic) {
			return line, nil
		}
	}
	return "", fmt.Errorf("no %s… line found on input", ExportMagic)
}

func MergeBundle(b *PortableBundle) int {
	added := 0
	for _, p := range b.Profiles {
		if existing := ProfileNameForAccount(p.Tool, p.Account); existing != "" {
			fmt.Printf("keep   %s/%s  (%s already present as %q)\n", p.Tool, p.Name, p.Account, existing)
			continue
		}
		orig := p.Name
		if _, err := os.Stat(BundlePath(p.Tool, p.Name)); err == nil || !os.IsNotExist(err) {
			p.Name = UniqueProfileName(p.Tool, orig, p.Account)
			fmt.Printf("rename %s/%s already used -> importing as %q\n", p.Tool, orig, p.Name)
		}
		_ = os.MkdirAll(ProfileDir(p.Tool), 0o700)
		_ = WriteBundle(p.Tool, p.Name, p.Entries)
		meta := types.ProfileMeta{Name: p.Name, Tool: p.Tool, Account: p.Account, Saved: p.Saved}
		mb, _ := json.MarshalIndent(meta, "", "  ")
		_ = WriteFileAtomic(MetaPath(p.Tool, p.Name), mb, 0o600)
		acctDisplay := p.Account
		if acctDisplay == "" {
			acctDisplay = "-"
		}
		fmt.Printf("add    %s/%s  (%s)\n", p.Tool, p.Name, acctDisplay)
		added++
	}
	return added
}

func UniqueProfileName(tool, base, account string) string {
	free := func(n string) bool {
		_, err := os.Stat(BundlePath(tool, n))
		return os.IsNotExist(err)
	}
	if account != "" && !strings.Contains(base, account) {
		if c := SanitizeName(base + "-" + account); free(c) {
			return c
		}
	}
	for i := 2; ; i++ {
		if c := fmt.Sprintf("%s-%d", base, i); free(c) {
			return c
		}
	}
}

func TrashProfile(tool, name string) error {
	_ = os.MkdirAll(TrashDir(tool), 0o700)
	ts := time.Now().Format("20060102-150405")
	for _, ext := range []string{".amp", ".meta.json"} {
		src := filepath.Join(ProfileDir(tool), name+ext)
		dst := filepath.Join(TrashDir(tool), fmt.Sprintf("%s__%s%s", ts, name, ext))
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}
	if ReadActivePointer(tool) == name {
		_ = os.Remove(ActivePath(tool))
	}
	return nil
}

func RestoreProfile(tool, q string) error {
	des, err := os.ReadDir(TrashDir(tool))
	if err != nil {
		return fmt.Errorf("nothing in trash for %s matching %q", tool, q)
	}
	type item struct {
		stamp string
		name  string
		base  string
	}
	var found []item
	for _, de := range des {
		n := de.Name()
		if !strings.HasSuffix(n, ".meta.json") {
			continue
		}
		body := strings.TrimSuffix(n, ".meta.json")
		var stamp, name string
		if s, nm, ok := strings.Cut(body, "__"); ok {
			stamp = s
			name = nm
		} else if i := strings.LastIndex(body, "."); i > 0 {
			name = body[:i]
			stamp = body[i+1:]
		} else {
			continue
		}
		if q == "" || strings.Contains(strings.ToLower(name), strings.ToLower(q)) {
			found = append(found, item{stamp, name, body})
		}
	}
	if len(found) == 0 {
		return fmt.Errorf("nothing in trash for %s matching %q", tool, q)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].stamp > found[j].stamp })
	it := found[0]
	if _, err := os.Stat(BundlePath(tool, it.name)); err == nil {
		return fmt.Errorf("%s/%s already exists — remove or rename it first", tool, it.name)
	}
	_ = os.MkdirAll(ProfileDir(tool), 0o700)
	_ = os.Rename(filepath.Join(TrashDir(tool), it.base+".amp"), BundlePath(tool, it.name))
	_ = os.Rename(filepath.Join(TrashDir(tool), it.base+".meta.json"), MetaPath(tool, it.name))
	fmt.Printf("restored %s/%s\n", tool, it.name)
	return nil
}
