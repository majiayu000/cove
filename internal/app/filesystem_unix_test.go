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
