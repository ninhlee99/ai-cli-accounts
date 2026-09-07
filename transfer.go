package main

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

	"golang.org/x/crypto/scrypt"
	"golang.org/x/term"
)

// Portable, machine-independent profile bundle. Unlike the on-disk profiles
// (encrypted with a per-machine Keychain key), an export is encrypted with a
// key derived from a passphrase, so it can be imported anywhere.
//
// Wire format (one line):
//   AMEXP1.<base64url( salt[16] | nonce[12] | AES-256-GCM( gzip(json) ) )>

const exportMagic = "AMEXP1."

type portableProfile struct {
	Tool    string    `json:"tool"`
	Name    string    `json:"name"`
	Account string    `json:"account"`
	Saved   time.Time `json:"saved"`
	Entries []entry   `json:"entries"` // decrypted artifact payloads
}

type portableBundle struct {
	Version   int               `json:"v"`
	Exported  time.Time         `json:"exported"`
	Profiles  []portableProfile `json:"profiles"`
}

func deriveKey(pass, salt []byte) []byte {
	k, err := scrypt.Key(pass, salt, 1<<15, 8, 1, 32)
	if err != nil {
		die("scrypt: %v", err)
	}
	return k
}

func readPassphrase(prompt string) []byte {
	fmt.Fprint(os.Stderr, prompt)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			die("read passphrase: %v", err)
		}
		return b
	}
	// Non-interactive: take one line from stdin, or $AM_PASSPHRASE.
	if v := os.Getenv("AM_PASSPHRASE"); v != "" {
		fmt.Fprintln(os.Stderr, "(from $AM_PASSPHRASE)")
		return []byte(v)
	}
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return []byte(strings.TrimRight(line, "\r\n"))
}

// ---- export ----

func cmdExport(args []string) {
	// args: [tool] [name ...]   (no args = everything)
	var wantTool string
	wantNames := map[string]bool{}
	if len(args) > 0 {
		wantTool = args[0]
		for _, n := range args[1:] {
			wantNames[n] = true
		}
	}

	bundle := collectBundle(wantTool, wantNames)
	if len(bundle.Profiles) == 0 {
		die("no profiles to export")
	}

	pass := readPassphrase("passphrase to encrypt the export: ")
	pass2 := readPassphrase("repeat passphrase: ")
	if !bytes.Equal(pass, pass2) {
		die("passphrases do not match")
	}

	blob := sealBundle(bundle, pass, true) // pass = raw passphrase; sealBundle derives
	fmt.Println(blob)
	fmt.Fprintf(os.Stderr, "\nexported %d profile(s). Copy the line above to the other machine and run: am import\n", len(bundle.Profiles))
}

func collectBundle(wantTool string, wantNames map[string]bool) portableBundle {
	var b portableBundle
	b.Version = 1
	b.Exported = time.Now()
	for _, tn := range toolNames(loadConfig()) {
		if wantTool != "" && tn != wantTool {
			continue
		}
		for _, m := range listProfiles(tn) {
			if strings.HasPrefix(m.Name, "_") {
				continue
			}
			if len(wantNames) > 0 && !wantNames[m.Name] {
				continue
			}
			b.Profiles = append(b.Profiles, portableProfile{
				Tool: tn, Name: m.Name, Account: m.Account, Saved: m.Saved,
				Entries: loadProfileEntries(tn, m.Name),
			})
		}
	}
	return b
}

// sealBundle gzips + AES-256-GCM encrypts the bundle. When saltInline is true
// the key is passphrase-derived and a fresh salt is prepended; otherwise the
// caller's key is used as-is (16 zero bytes stand in for the salt slot).
func sealBundle(b portableBundle, key []byte, saltInline bool) string {
	plain, _ := json.Marshal(b)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(plain)
	_ = zw.Close()

	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if saltInline {
		_, _ = rand.Read(salt)
		key = deriveKey(key, salt) // key here is the raw passphrase bytes
	}
	_, _ = rand.Read(nonce)

	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	ct := gcm.Seal(nil, nonce, gz.Bytes(), nil)
	out := append(append(salt, nonce...), ct...)
	return exportMagic + base64.URLEncoding.EncodeToString(out)
}

// autoBackup writes an encrypted snapshot of every profile to
// ~/.am/backups/, keyed by the machine's master key (no passphrase — it's a
// local safety net, not for transfer). Keeps the 20 most recent.
func autoBackup() {
	b := collectBundle("", nil)
	if len(b.Profiles) == 0 {
		return
	}
	dir := filepath.Join(baseDir(), "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	blob := sealBundle(b, masterKey(), false)
	name := time.Now().Format("2006-01-02_150405") + ".amexp"
	_ = os.WriteFile(filepath.Join(dir, name), []byte(blob), 0o600)
	pruneBackups(dir, 20)
}

// cmdRestoreBackup re-imports the most recent auto-backup (or one named by
// substring). Additive — same merge rules as `am import`.
func cmdRestoreBackup(which string) {
	dir := filepath.Join(baseDir(), "backups")
	des, _ := os.ReadDir(dir)
	var files []string
	for _, de := range des {
		if strings.HasSuffix(de.Name(), ".amexp") && (which == "" || strings.Contains(de.Name(), which)) {
			files = append(files, de.Name())
		}
	}
	if len(files) == 0 {
		die("no auto-backups in %s", dir)
	}
	sort.Strings(files)
	pick := files[len(files)-1]
	blob, err := os.ReadFile(filepath.Join(dir, pick))
	if err != nil {
		die("read backup: %v", err)
	}
	b := openBundle(strings.TrimSpace(string(blob)), masterKey(), false)
	n := mergeBundle(b)
	fmt.Printf("restored from %s: %d profile(s) added\n", pick, n)
}

// openBundle decrypts a sealed blob. When saltInline the key is the raw
// passphrase and the salt is read from the blob; otherwise key is used directly.
func openBundle(blob string, key []byte, saltInline bool) portableBundle {
	raw, err := base64.URLEncoding.DecodeString(strings.TrimPrefix(blob, exportMagic))
	if err != nil {
		die("bad blob (base64): %v", err)
	}
	if len(raw) < 16+12+16 {
		die("bad blob (too short)")
	}
	salt, nonce, ct := raw[:16], raw[16:28], raw[28:]
	if saltInline {
		key = deriveKey(key, salt)
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	gz, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		die("decrypt failed (wrong passphrase or key?)")
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		die("gunzip: %v", err)
	}
	plain, _ := io.ReadAll(zr)
	var b portableBundle
	if json.Unmarshal(plain, &b) != nil {
		die("bad bundle json")
	}
	return b
}

func pruneBackups(dir string, keep int) {
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

// ---- import ----

func cmdImport(args []string) {
	activate := map[string]string{} // tool -> profile name to `am use` after import
	var src io.Reader = os.Stdin
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--activate":
			i++
			t, n, _ := strings.Cut(args[i], "=")
			activate[t] = n
		case "--file", "-f":
			i++
			f, err := os.Open(args[i])
			if err != nil {
				die("open %s: %v", args[i], err)
			}
			defer f.Close()
			src = f
		default:
			die("unknown flag %q", args[i])
		}
	}

	blob := readExportBlob(src)
	pass := readPassphrase("passphrase to decrypt the import: ")
	bundle := openBundle(blob, pass, true)

	added := mergeBundle(bundle)
	kept := len(bundle.Profiles) - added
	fmt.Printf("\nimport done: %d added, %d already present (kept).\n", added, kept)

	for tool, name := range activate {
		resolved := resolveName(tool, name)
		fmt.Printf("activating %s -> %s\n", tool, resolved)
		cmdUse(tool, resolved)
	}
	if len(activate) == 0 {
		fmt.Println("nothing was applied to the system; run `am sw <id>` to switch to one.")
	}
}

// mergeBundle adds profiles that aren't present, keeping existing ones
// untouched. Returns how many were added.
func mergeBundle(b portableBundle) int {
	added := 0
	for _, p := range b.Profiles {
		if existing := profileNameForAccount(p.Tool, p.Account); existing != "" {
			fmt.Printf("keep   %s/%s  (%s already present as %q)\n", p.Tool, p.Name, p.Account, existing)
			continue
		}
		orig := p.Name
		if _, err := os.Stat(bundlePath(p.Tool, p.Name)); err == nil {
			p.Name = uniqueProfileName(p.Tool, orig, p.Account)
			fmt.Printf("rename %s/%s already used -> importing as %q\n", p.Tool, orig, p.Name)
		}
		writePortableProfile(p)
		fmt.Printf("add    %s/%s  (%s)\n", p.Tool, p.Name, orDash(p.Account))
		added++
	}
	return added
}

func readExportBlob(r io.Reader) string {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, exportMagic) {
			return line
		}
	}
	die("no %s… line found on input (paste the export blob, then Ctrl-D)", exportMagic)
	return ""
}

// uniqueProfileName finds a free profile name for an import that collides with
// an existing (differently-accounted) name: "<base>-<account>", then
// "<base>-2", "<base>-3", …
func uniqueProfileName(tool, base, account string) string {
	free := func(n string) bool {
		_, err := os.Stat(bundlePath(tool, n))
		return os.IsNotExist(err)
	}
	if account != "" && !strings.Contains(base, account) {
		if c := sanitizeName(base + "-" + account); free(c) {
			return c
		}
	}
	for i := 2; ; i++ {
		if c := fmt.Sprintf("%s-%d", base, i); free(c) {
			return c
		}
	}
}

func profileNameForAccount(tool, account string) string {
	if account == "" {
		return ""
	}
	for _, m := range listProfiles(tool) {
		if strings.EqualFold(m.Account, account) {
			return m.Name
		}
	}
	return ""
}

func writePortableProfile(p portableProfile) {
	if err := os.MkdirAll(profileDir(p.Tool), 0o700); err != nil {
		die("mkdir: %v", err)
	}
	enc := encrypt(packEntries(p.Entries)) // re-encrypt with THIS machine's key
	if err := writeFileAtomic(bundlePath(p.Tool, p.Name), enc, 0o600); err != nil {
		die("write bundle: %v", err)
	}
	m := profileMeta{Name: p.Name, Tool: p.Tool, Account: p.Account, Saved: p.Saved}
	mb, _ := json.MarshalIndent(m, "", "  ")
	_ = writeFileAtomic(metaPath(p.Tool, p.Name), mb, 0o600)
}
