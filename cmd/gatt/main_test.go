package main

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"gatt/internal/app"
)

func TestOpenManagementRequiresReadiness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			t.Errorf("open checked %s instead of readiness", r.URL.Path)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if err := openManagement(app.Config{Listen: strings.TrimPrefix(server.URL, "http://")}); err == nil {
		t.Fatal("management opened before readiness")
	}
}

func TestDataDirectoryLockAcrossProcesses(t *testing.T) {
	if dir := os.Getenv("GATT_TEST_LOCK_DIR"); dir != "" {
		lock, err := lockDataDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		os.Stdout.WriteString("locked\n")
		time.Sleep(time.Hour)
		return
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDataDirectoryLockAcrossProcesses$")
	child.Env = append(os.Environ(), "GATT_TEST_LOCK_DIR="+dir)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || ready != "locked\n" {
		t.Fatal("child did not acquire directory lock")
	}
	if lock, err := lockDataDir(dir); err == nil {
		lock.Close()
		t.Fatal("second process admitted")
	}
	otherDir := t.TempDir()
	if err := os.Chmod(otherDir, 0700); err != nil {
		t.Fatal(err)
	}
	other, err := lockDataDir(otherDir)
	if err != nil {
		t.Fatal("independent directory blocked")
	}
	other.Close()
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	lock, err := lockDataDir(dir)
	if err != nil {
		t.Fatal("crashed process left a stale lock")
	}
	lock.Close()
}
