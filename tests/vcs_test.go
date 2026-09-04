package tests

import (
	"os/exec"
	"testing"

	"github.com/memor-dev/memor/internal/vcs"
)

// gitRepo turns dir into a git repository with one commit, or skips the test
// when git is unavailable. Identity is set locally so the test never depends on
// the developer's global git config.
func gitRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@memor.dev"},
		{"config", "user.name", "memor test"},
		{"add", "-A"},
		{"-c", "commit.gpgsign=false", "commit", "-q", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v failed: %v: %s", args, err, out)
		}
	}
}

func TestValidSHARejectsNonHex(t *testing.T) {
	// Revisions reach git as command arguments, so anything that is not a plain
	// object name must be refused before it gets there.
	for _, bad := range []string{"", "HEAD", "main", "--upload-pack=touch", "abc", "zzzzzzz", "a1b2c3d;rm -rf /"} {
		if vcs.ValidSHA(bad) {
			t.Errorf("expected %q to be rejected as a revision", bad)
		}
	}
	if !vcs.ValidSHA("a1b2c3d") || !vcs.ValidSHA("0123456789abcdef0123456789abcdef01234567") {
		t.Error("expected plain hex object names to be accepted")
	}
}

func TestChangedSinceRejectsAnInvalidRevision(t *testing.T) {
	root := sampleRepo(t)
	gitRepo(t, root)

	if _, err := vcs.ChangedSince(root, "HEAD~1; echo pwned"); err == nil {
		t.Error("expected an invalid revision to be refused")
	}
}

func TestHeadAndDirtyReportWorkingTreeState(t *testing.T) {
	root := sampleRepo(t)
	gitRepo(t, root)

	if !vcs.Available(root) {
		t.Fatal("expected the directory to be a work tree")
	}
	head, err := vcs.ReadHead(root)
	if err != nil {
		t.Fatalf("ReadHead: %v", err)
	}
	if !vcs.ValidSHA(head.SHA) {
		t.Errorf("expected a commit SHA, got %q", head.SHA)
	}

	if dirty, err := vcs.Dirty(root); err != nil || len(dirty) != 0 {
		t.Errorf("expected a clean tree, got %v (%v)", dirty, err)
	}

	writeFile(t, root, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, root, "brand_new.go", "package main\n")

	dirty, err := vcs.Dirty(root)
	if err != nil {
		t.Fatalf("Dirty: %v", err)
	}
	got := make(map[string]vcs.Status, len(dirty))
	for _, c := range dirty {
		got[c.Path] = c.Status
	}
	if got["main.go"] != vcs.StatusModified {
		t.Errorf("expected main.go to be modified, got %q", got["main.go"])
	}
	if got["brand_new.go"] != vcs.StatusUntracked {
		t.Errorf("expected brand_new.go to be untracked, got %q", got["brand_new.go"])
	}
}

func TestChangedSinceIncludesCommittedAndUncommitted(t *testing.T) {
	root := sampleRepo(t)
	gitRepo(t, root)

	head, err := vcs.ReadHead(root)
	if err != nil {
		t.Fatalf("ReadHead: %v", err)
	}

	writeFile(t, root, "internal/store/store.go", "package store\n\nfunc Load() string { return \"\" }\n")
	changes, err := vcs.ChangedSince(root, head.SHA)
	if err != nil {
		t.Fatalf("ChangedSince: %v", err)
	}

	found := false
	for _, c := range changes {
		if c.Path == "internal/store/store.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the edited file to be reported, got %v", changes)
	}

	if n, err := vcs.CommitsBetween(root, head.SHA); err != nil || n != 0 {
		t.Errorf("expected no commits since HEAD, got %d (%v)", n, err)
	}
}

// Outside a work tree every read must fail cleanly rather than hang or panic,
// because memor still has to work in a plain directory.
func TestNonGitDirectoryDegradesCleanly(t *testing.T) {
	root := t.TempDir()

	if vcs.Available(root) {
		t.Skip("temp dir is inside a git work tree")
	}
	if _, err := vcs.ReadHead(root); err == nil {
		t.Error("expected ReadHead to fail outside a repository")
	}
}
