//go:build !windows

package app

import (
	"os"
	"path/filepath"
	"testing"
)

func installClientVersionFixture(t *testing.T, dir, name, version string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nprintf '%s\\n' '"+version+"'\n"), 0700); err != nil {
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
