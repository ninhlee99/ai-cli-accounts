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

	var bundle portableBundle
	bundle.Version = 1
	bundle.Exported = time.Now()

	c := loadConfig()
	for _, tn := range toolNames(c) {
		if wantTool != "" && tn != wantTool {
			continue
		}
		for _, m := range listProfiles(tn) {
			if strings.HasPrefix(m.Name, "_") {
				continue // skip _prev / autosave scratch profiles
			}
			if len(wantNames) > 0 && !wantNames[m.Name] {
				continue
			}
			bundle.Profiles = append(bundle.Profiles, portableProfile{
				Tool: tn, Name: m.Name, Account: m.Account, Saved: m.Saved,
				Entries: loadProfileEntries(tn, m.Name),
			})
		}
	}
	if len(bundle.Profiles) == 0 {
		die("no profiles to export")
	}

	pass := readPassphrase("passphrase to encrypt the export: ")
	pass2 := readPassphrase("repeat passphrase: ")
	if !bytes.Equal(pass, pass2) {
		die("passphrases do not match")
	}

	plain, _ := json.Marshal(bundle)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(plain)
	_ = zw.Close()

	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	_, _ = rand.Read(salt)
	_, _ = rand.Read(nonce)

	block, _ := aes.NewCipher(deriveKey(pass, salt))
	gcm, _ := cipher.NewGCM(block)
	ct := gcm.Seal(nil, nonce, gz.Bytes(), nil)

	out := append(append(salt, nonce...), ct...)
	fmt.Println(exportMagic + base64.URLEncoding.EncodeToString(out))

	fmt.Fprintf(os.Stderr, "\nexported %d profile(s). Copy the line above to the other machine and run: am import\n", len(bundle.Profiles))
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

	raw, err := base64.URLEncoding.DecodeString(strings.TrimPrefix(blob, exportMagic))
	if err != nil {
		die("bad blob (base64): %v", err)
	}
	if len(raw) < 16+12+16 {
		die("bad blob (too short)")
	}
	salt, nonce, ct := raw[:16], raw[16:28], raw[28:]

	block, _ := aes.NewCipher(deriveKey(pass, salt))
	gcm, _ := cipher.NewGCM(block)
	gz, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		die("decrypt failed (wrong passphrase?)")
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		die("gunzip: %v", err)
	}
	plain, _ := io.ReadAll(zr)

	var bundle portableBundle
	if err := json.Unmarshal(plain, &bundle); err != nil {
		die("bad bundle json: %v", err)
	}

	added, kept := 0, 0
	for _, p := range bundle.Profiles {
		// "Already logged in" == a profile for this tool + account already exists.
		if existing := profileNameForAccount(p.Tool, p.Account); existing != "" {
			fmt.Printf("keep   %s/%s  (%s already present as %q)\n", p.Tool, p.Name, p.Account, existing)
			kept++
			continue
		}
		// Same name, different account: keep the existing one, rename the import.
		orig := p.Name
		if _, err := os.Stat(bundlePath(p.Tool, p.Name)); err == nil {
			p.Name = uniqueProfileName(p.Tool, orig, p.Account)
			fmt.Printf("rename %s/%s already used -> importing as %q\n", p.Tool, orig, p.Name)
		}
		writePortableProfile(p)
		fmt.Printf("add    %s/%s  (%s)\n", p.Tool, p.Name, orDash(p.Account))
		added++
	}

	fmt.Printf("\nimport done: %d added, %d already present (kept).\n", added, kept)

	for tool, name := range activate {
		resolved := resolveName(tool, name)
		fmt.Printf("activating %s -> %s\n", tool, resolved)
		cmdUse(tool, resolved)
	}
	if len(activate) == 0 {
		fmt.Println("nothing was applied to the system; run `am use <tool> <name>` to switch to one.")
	}
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
