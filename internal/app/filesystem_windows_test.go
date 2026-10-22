//go:build windows

package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestSpecWindowsSecretsACLAndCapacity(t *testing.T) {
	dir := t.TempDir()
	vault, err := NewFileSecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = vault.Put("test", "synthetic"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Dir(vault.path), vault.path} {
		assertPrivateTestPermissions(t, path, 0600)
	}
	if free, err := resourceFreeSpace(context.Background(), dir); err != nil || free == 0 {
		t.Fatalf("native capacity unavailable: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resourceFreeSpace(ctx, dir); err != context.Canceled {
		t.Fatal("cancelled capacity probe did not preserve cancellation")
	}
}

func TestSpecWindowsClientFileACLAndRootPublication(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	target, err := openClientFile(dir, path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer target.close()
	expected := "missing"
	for _, content := range []string{`{"model":"first"}`, `{"model":"replacement"}`} {
		changed, err := target.replace([]byte(content), expected, false)
		if err != nil || !changed {
			t.Fatalf("private client publication: changed=%v error=%v", changed, err)
		}
		actual, exists, err := target.read()
		if err != nil || !exists || string(actual) != content {
			t.Fatal("published client content differs", err)
		}
		assertPrivateTestPermissions(t, path, 0600)
		expected = clientHash(actual, exists)
	}
}

var clientVersionFixtureBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cove-version-fixture-")
	if err != nil {
		panic(err)
	}
	// Compile a tiny version printer once. Copying the race test executable
	// repeats all application/SDK initialization and race exit delays per probe.
	clientVersionFixtureBinary = filepath.Join(dir, "client-version.exe")
	command := exec.Command("go", "build", "-o", clientVersionFixtureBinary, "./testdata/client-version")
	code := 1
	if output, err := command.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build client version fixture: %v\n%s", err, output)
	} else {
		code = m.Run()
	}
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintln(os.Stderr, "remove client version fixture:", err)
		code = 1
	}
	os.Exit(code)
}
func installClientVersionFixture(t *testing.T, dir, name, version string) {
	t.Helper()
	target := filepath.Join(dir, name+".exe")
	if _, err := os.Stat(target); os.IsNotExist(err) {
		content, err := os.ReadFile(clientVersionFixtureBinary)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, content, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".cove-fixture-"+name+"-version"), []byte(version), 0600); err != nil {
		t.Fatal(err)
	}
}
func assertPrivateTestPermissions(t *testing.T, path string, _ os.FileMode) {
	t.Helper()
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		t.Fatalf("credential ACL does not restrict access to one principal: %s", path)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	owner, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !sid.Equals(owner.User.Sid) || ace.Mask != fileAllAccess && ace.Mask != fileAllAccess&^windows.FILE_EXECUTE {
		t.Fatalf("private ACL grants another principal access: %s", path)
	}
}

func assertClientScriptNotExecutable(t *testing.T, path string) {
	t.Helper()
	assertPrivateTestPermissions(t, path, 0600)
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		t.Fatal(err)
	}
	if ace.Mask&windows.FILE_EXECUTE != 0 {
		t.Fatal("copied script grants native execute permission")
	}
}

// A real Windows handle permits preflight reads and denies atomic replacement.
func blockClientFileReplacement(t *testing.T, path string) func() {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			if err := windows.CloseHandle(handle); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(release)
	return release
}
