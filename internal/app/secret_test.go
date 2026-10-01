package app

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestFileSecretsPersistenceAndPermissions(t *testing.T) {
	dir := t.TempDir()
	v, err := NewFileSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Put("administrator", "synthetic-admin"); err != nil {
		t.Fatal(err)
	}
	if err := v.Put("source", "synthetic-source"); err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{filepath.Dir(v.path): 0700, v.path: 0600} {
		assertPrivateTestPermissions(t, path, mode)
	}
	if err := v.Put("source", "replacement"); !errors.Is(err, os.ErrExist) {
		t.Fatal("existing credential overwritten")
	}
	if err := v.Delete("source"); err != nil {
		t.Fatal(err)
	}
	v, err = NewFileSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v.Get("administrator"); err != nil || got != "synthetic-admin" {
		t.Fatal("administrator not preserved across restart")
	}
	if _, err := v.Get("source"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deleted credential returned after restart")
	}
	if err := v.Delete("source"); err != nil {
		t.Fatal("missing item cleanup must be idempotent")
	}
}

func TestFileSecretsFailedWriteDoesNotPublish(t *testing.T) {
	v, err := NewFileSecrets(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Put("old", "synthetic"); err != nil {
		t.Fatal(err)
	}
	// A directory in place of the destination forces rename failure even as root.
	if err := os.Remove(v.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(v.path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := v.Put("new", "synthetic-new"); err == nil {
		t.Fatal("failed write reported success")
	}
	if _, err := v.Get("new"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed write published to cache")
	}
	if err := v.Delete("old"); err == nil {
		t.Fatal("failed deletion reported success")
	}
	if got, err := v.Get("old"); err != nil || got != "synthetic" {
		t.Fatal("failed deletion discarded old value")
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(v.path), ".credentials-*"))
	if err != nil || len(files) != 0 {
		t.Fatal("temporary credentials left behind")
	}
}

func TestFileSecretsCorruptionAndSymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	v, err := NewFileSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v.path, []byte("invalid credential JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileSecrets(dir); err == nil {
		t.Fatal("corrupt credentials silently reset")
	}
	if err := os.Remove(v.path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, v.path); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileSecrets(dir); err == nil {
		t.Fatal("credential symlink accepted")
	}
}

func TestFileSecretsConcurrentUpdates(t *testing.T) {
	dir := t.TempDir()
	v, err := NewFileSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, ref := range []string{"one", "two", "three", "four"} {
		wg.Go(func() {
			if err := v.Put(ref, "synthetic"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	reopened, err := NewFileSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.values) != 4 {
		t.Fatal("concurrent update lost credentials")
	}
}
