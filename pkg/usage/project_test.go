package usage

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitRootOrSelf_ResolvesFromNestedSubdir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	repoRoot, err := os.MkdirTemp("", "am-usage-test-repo-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(repoRoot)
	// Resolve symlinks (e.g. macOS /tmp -> /private/tmp) so the comparison
	// below matches what `git rev-parse --show-toplevel` itself prints.
	repoRoot, err = filepath.EvalSymlinks(repoRoot)
	if err != nil {
		t.Fatal(err)
	}

	if out, err := exec.Command("git", "-C", repoRoot, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v: %s", err, out)
	}

	nested := filepath.Join(repoRoot, "pkg", "usage")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}

	if got := gitRootOrSelf(nested); got != repoRoot {
		t.Errorf("gitRootOrSelf(%q) = %q, want repo root %q", nested, got, repoRoot)
	}
	// Called from the root itself, it's already the toplevel.
	if got := gitRootOrSelf(repoRoot); got != repoRoot {
		t.Errorf("gitRootOrSelf(%q) = %q, want %q", repoRoot, got, repoRoot)
	}
}

func TestGitRootOrSelf_FallsBackOutsideGitRepo(t *testing.T) {
	nonRepo, err := os.MkdirTemp("", "am-usage-test-noRepo-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(nonRepo)
	nonRepo, err = filepath.EvalSymlinks(nonRepo)
	if err != nil {
		t.Fatal(err)
	}

	if got := gitRootOrSelf(nonRepo); got != nonRepo {
		t.Errorf("gitRootOrSelf(%q) = %q, want unchanged %q", nonRepo, got, nonRepo)
	}
}
