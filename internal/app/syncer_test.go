package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSyncResetsCheckoutAndMirrorsOnlyXML(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	author := filepath.Join(root, "author")
	checkout := filepath.Join(root, "checkout")
	destination := filepath.Join(root, "destination")

	run(t, root, "git", "init", "--bare", remote)
	run(t, root, "git", "clone", remote, author)
	run(t, author, "git", "config", "user.email", "test@example.com")
	run(t, author, "git", "config", "user.name", "Test")
	run(t, author, "git", "switch", "-c", "main")
	if err := os.Mkdir(filepath.Join(author, "Lowess"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(author, "Lowess", "one.xml"), "<one/>\n")
	write(t, filepath.Join(author, "Lowess", "ignored.txt"), "ignored\n")
	run(t, author, "git", "add", ".")
	run(t, author, "git", "commit", "-m", "initial")
	run(t, author, "git", "push", "-u", "origin", "main")
	run(t, root, "git", "clone", "--branch", "main", remote, checkout)

	write(t, filepath.Join(checkout, "untracked.txt"), "remove me\n")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(destination, "stale.xml"), "<stale/>\n")
	write(t, filepath.Join(destination, "keep.txt"), "keep\n")

	config := Config{
		GitHubBranch:      "main",
		GitRemoteURL:      remote,
		GitRemoteTracking: "origin",
		RepoDir:           checkout,
		SourceDir:         filepath.Join(checkout, "Lowess"),
		DestinationDir:    destination,
		SyncTimeout:       30 * time.Second,
		GitClean:          true,
	}
	commit, err := NewGitSynchronizer(config).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if commit == "" {
		t.Fatal("expected commit")
	}
	assertContent(t, filepath.Join(destination, "one.xml"), "<one/>\n")
	assertContent(t, filepath.Join(destination, "keep.txt"), "keep\n")
	assertMissing(t, filepath.Join(destination, "stale.xml"))
	assertMissing(t, filepath.Join(destination, "ignored.txt"))
	assertMissing(t, filepath.Join(checkout, "untracked.txt"))
}

func run(t *testing.T, directory, name string, arguments ...string) string {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertContent(t *testing.T, path, expected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != expected {
		t.Fatalf("unexpected content for %s: %q", path, content)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be missing, got %v", path, err)
	}
}
