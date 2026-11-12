//go:build !windows

package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSpecClientVersionChildPipeWaitBounded(t *testing.T) {
	dir := t.TempDir()
	command := "#!/bin/sh\n/bin/sleep 3 &\nprintf '%s\\n' 'codex-cli 0.160.0'\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(command), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	began := time.Now()
	card := detectClient(context.Background(), clientCard{Kind: "codex", ContractVersion: "0.160.0"})
	if card.Status != "detection_failed" || time.Since(began) >= 2500*time.Millisecond {
		t.Fatal("version descendant pipe was not bounded", card.Status, time.Since(began))
	}
}

var clientVersionFixtureDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cove-version-fixture-")
	if err != nil {
		panic(err)
	}
	clientVersionFixtureDir = dir
	code := m.Run()
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintln(os.Stderr, "remove client version fixture:", err)
		code = 1
	}
	os.Exit(code)
}

// macOS scans every new executable on its first run. A fresh script per test
// queued those scans past the detection timeout, so each version printer is
// written once and linked into the test's PATH directory.
func installClientVersionFixture(t *testing.T, dir, name, version string) {
	t.Helper()
	script := filepath.Join(clientVersionFixtureDir, fmt.Sprintf("%x", sha256.Sum256([]byte(version))))
	if _, err := os.Stat(script); os.IsNotExist(err) {
		if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '"+version+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(dir, name)
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Symlink(script, target); err != nil {
		t.Fatal(err)
	}
}
func assertPrivateTestPermissions(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != mode {
		t.Fatalf("private permissions not applied: %s", path)
	}
}

func assertClientScriptNotExecutable(t *testing.T, path string) {
	t.Helper()
	assertPrivateTestPermissions(t, path, 0600)
}

// Leave the selected file readable for preflight, but fail its atomic publish.
func blockClientFileReplacement(t *testing.T, path string) func() {
	t.Helper()
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0500); err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			if err := os.Chmod(parent, info.Mode().Perm()); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(release)
	return release
}
