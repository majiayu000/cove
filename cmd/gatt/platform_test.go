package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
func assertPrivatePlatformPath(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("private path permission mismatch: %s %v", path, err)
		}
		return
	}
	script := `$ErrorActionPreference='Stop'; $acl=Get-Acl -LiteralPath $inputData.path; $rules=@($acl.GetAccessRules($true,$true,[Security.Principal.SecurityIdentifier])); $owner=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value; if(-not $acl.AreAccessRulesProtected -or $rules.Count -ne 1 -or $rules[0].IdentityReference.Value -ne $owner -or $rules[0].AccessControlType -ne 'Allow' -or ($rules[0].FileSystemRights -band [Security.AccessControl.FileSystemRights]::FullControl) -ne [Security.AccessControl.FileSystemRights]::FullControl){throw 'Path is not restricted to the current user'}; 'private'`
	output, err := nativePlatformRunner(context.Background(), "powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", powershellEncoded(script, map[string]string{"path": path}))
	if err != nil || strings.TrimSpace(string(output)) != "private" {
		t.Fatalf("private Windows ACL mismatch: %s %v %s", path, err, output)
	}
}

func TestSpecPrivateJournalPublication(t *testing.T) {
	dir := privateTempDir(t)
	if err := protectPlatformPath(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "journal.json")
	for _, data := range []string{"first", "replacement"} {
		if err := writePrivate(path, []byte(data)); err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(path)
		if err != nil || string(content) != data {
			t.Fatal("journal publication failed", err)
		}
		assertPrivatePlatformPath(t, path, 0600)
	}
}
func fakePlatform(t *testing.T, goos string) platformEnvironment {
	t.Helper()
	root := privateTempDir(t)
	exe := filepath.Join(root, "Cove 中文", "gatt")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.MkdirAll(filepath.Dir(exe), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("old binary"), 0700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "数据 空格")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return platformEnvironment{OS: goos, Arch: "arm64", Home: root, UserID: "test-user", Executable: exe, ConfigPath: filepath.Join(root, "配置 file.json"), DataDir: dir, Listen: "127.0.0.1:54321", Output: &bytes.Buffer{}, Client: &http.Client{Timeout: time.Second}, Run: func(context.Context, string, ...string) ([]byte, error) { return nil, errServiceAbsent }}
}
func TestSpecPlatformsBrowserAndDirectories(t *testing.T) {
	for _, tc := range []struct {
		goos, command string
		args          []string
	}{{"darwin", "open", []string{"http://127.0.0.1:1234"}}, {"linux", "xdg-open", []string{"http://127.0.0.1:1234"}}, {"windows", "rundll32.exe", []string{"url.dll,FileProtocolHandler", "http://127.0.0.1:1234"}}} {
		name, args, err := browserCommand(tc.goos, "http://127.0.0.1:1234")
		if err != nil || name != tc.command || !reflect.DeepEqual(args, tc.args) {
			t.Fatalf("%s: %s %v %v", tc.goos, name, args, err)
		}
	}
	for _, address := range []string{"https://127.0.0.1:1", "http://evil.example:1", "http://user@127.0.0.1:1", "http://127.0.0.1:1?secret=yes"} {
		if _, _, err := browserCommand("darwin", address); err == nil {
			t.Fatalf("accepted %s", address)
		}
	}
	dir, err := platformDefaultDataDir("darwin", "/Users/中文 用户", "", "")
	if err != nil || !strings.HasSuffix(dir, filepath.Join("Application Support", "Cove")) {
		t.Fatal(dir, err)
	}
	dir, err = platformDefaultDataDir("linux", "/home/u", "/private/data", "")
	if err != nil || dir != filepath.Join("/private/data", "Cove") {
		t.Fatal(dir, err)
	}
	if _, err = platformDefaultDataDir("windows", "", "", ""); err == nil {
		t.Fatal("invented LOCALAPPDATA")
	}
	if windowsArg(`C:\中文 space\`) != `"C:\中文 space\\"` || windowsArg(`a"b`) != `"a\"b"` {
		t.Fatal("Windows CRT argument quoting")
	}
}
func TestSpecServiceLifecycleSyntheticManagers(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			env := fakePlatform(t, goos)
			loaded := false
			var calls []string
			env.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				calls = append(calls, name+" "+strings.Join(args, " "))
				if name == "launchctl" && args[0] == "print" {
					if !loaded {
						return nil, errServiceAbsent
					}
					return []byte("path = " + env.servicePath() + "\nCOVE_SERVICE_ID => " + env.serviceIdentity() + "\n"), nil
				}
				if name == "systemctl" && args[1] == "show" {
					if !loaded {
						return []byte("LoadState=not-found\n"), nil
					}
					return []byte("LoadState=loaded\nActiveState=inactive\nMainPID=0\nFragmentPath=" + env.servicePath() + "\nDropInPaths=\nEnvironment=COVE_SERVICE_ID=" + env.serviceIdentity() + "\n"), nil
				}
				if name == "powershell.exe" {
					if !loaded {
						return []byte(`{"exists":false}`), nil
					}
					document, err := env.serviceDocument()
					return mustPlatformJSON(map[string]any{"exists": true, "document": windowsTaskText(document), "state": 3, "run_level": 0}), err
				}
				if name == "systemctl" && args[1] == "daemon-reload" {
					_, err := os.Stat(env.servicePath())
					loaded = err == nil
				}
				if name == "schtasks.exe" && args[0] == "/Create" {
					loaded = true
				}
				if name == "launchctl" && args[0] == "bootstrap" {
					loaded = true
				}
				return nil, nil
			}
			ctx := context.Background()
			if err := env.service(ctx, "install"); err != nil {
				t.Fatal(err)
			}
			first, err := os.ReadFile(env.servicePath())
			if err != nil {
				t.Fatal(err)
			}
			if err = env.service(ctx, "install"); err != nil {
				t.Fatal(err)
			}
			second, _ := os.ReadFile(env.servicePath())
			if !bytes.Equal(first, second) {
				t.Fatal("idempotent install changed registration")
			}
			if err = env.service(ctx, "start"); err != nil {
				t.Fatal(err)
			}
			if err = env.service(ctx, "uninstall"); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(env.servicePath()); !os.IsNotExist(err) {
				t.Fatal("registration retained")
			}
			if _, err = os.Stat(env.DataDir); err != nil {
				t.Fatal("uninstall deleted data")
			}
			for _, call := range calls {
				if strings.Contains(call, "/End") || strings.Contains(call, "sudo") || strings.Contains(call, " /F") && !strings.Contains(call, " /Delete ") {
					t.Fatal("force/admin mutation", call)
				}
			}
		})
	}
}
func TestSpecServiceRegistrationConflictRefusesMutation(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		env := fakePlatform(t, goos)
		if err := writePrivate(env.servicePath(), []byte("some other service")); err != nil {
			t.Fatal(err)
		}
		called := false
		env.Run = func(context.Context, string, ...string) ([]byte, error) { called = true; return nil, nil }
		if err := env.service(context.Background(), "install"); err == nil {
			t.Fatal("accepted conflict", goos)
		}
		data, _ := os.ReadFile(env.servicePath())
		if called || string(data) != "some other service" {
			t.Fatal("modified conflicting registration")
		}
	}
	env := fakePlatform(t, "darwin")
	doc, _ := env.serviceDocument()
	_ = writePrivate(env.servicePath(), doc)
	env.Run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("path = other\nCOVE_SERVICE_ID => different"), nil
	}
	if _, _, _, err := env.managerStatus(context.Background()); err == nil {
		t.Fatal("accepted loaded registration different from file")
	}
}
func TestSpecServiceWindowsControlledXML(t *testing.T) {
	env := fakePlatform(t, "windows")
	data, err := env.serviceDocument()
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 2 || data[0] != 0xff || data[1] != 0xfe {
		t.Fatal("Windows task document must be UTF-16LE with BOM")
	}
	var task windowsTask
	if err = xml.Unmarshal([]byte(windowsTaskText(data)), &task); err != nil {
		t.Fatal(err)
	}
	if task.Principals.Principal.UserID != env.UserID || task.Principals.Principal.RunLevel != "LeastPrivilege" || len(task.Actions.Exec) != 1 || task.Actions.Exec[0].Command != env.Executable || strings.Contains(task.Actions.Exec[0].Arguments, "cmd.exe") {
		t.Fatalf("unsafe task: %+v", task)
	}
}
func TestSpecServiceStatusTruthAndGracefulControl(t *testing.T) {
	env := fakePlatform(t, "darwin")
	hash, _ := fileSHA(env.Executable)
	var record runtimePrivate
	var stops atomic.Int32
	lock, err := lockDataDir(env.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]any{"pid": 123, "build_id": "build123"})
		case "/readyz":
			w.WriteHeader(200)
		default:
			w.WriteHeader(404)
		}
	})
	server := httptest.NewServer(platformControlledHandler(next, &record, func() { stops.Add(1); lock.Close() }))
	defer server.Close()
	env.Listen = strings.TrimPrefix(server.URL, "http://")
	record = runtimePrivate{runtimeInstance{PID: 123, Executable: env.Executable, ConfigPath: env.ConfigPath, DataDir: env.DataDir, Listen: env.Listen, BuildID: "build123", BinarySHA: hash, StartedAt: time.Now()}, "synthetic-control-secret"}
	if err = writePrivate(filepath.Join(env.DataDir, "runtime.json"), mustPlatformJSON(record)); err != nil {
		t.Fatal(err)
	}
	env.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "launchctl" {
			return nil, errServiceAbsent
		}
		if name == "lsof" && strings.Contains(strings.Join(args, " "), "-Fp") {
			return []byte("p123\n"), nil
		}
		return []byte("n" + env.Executable + "\n"), nil
	}
	status, err := env.status(context.Background())
	if err != nil || !status.Verified || !status.Running || !status.Ready || status.PortOwnerPID == nil || *status.PortOwnerPID != 123 {
		t.Fatal(status, err)
	}
	if bytes.Contains(mustPlatformJSON(status), []byte(record.ControlToken)) {
		t.Fatal("control secret exposed")
	}
	unauthorized, err := http.Post(server.URL+"/__cove/stop", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != 403 || stops.Load() != 0 {
		t.Fatal("unauthorized stop")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = env.stopInstance(ctx); err != nil {
		t.Fatal(err)
	}
	if stops.Load() != 1 {
		t.Fatal("graceful stop missing")
	}
}
func TestSpecServiceUnknownIdentityCannotStop(t *testing.T) {
	env := fakePlatform(t, "darwin")
	lock, err := lockDataDir(env.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = env.stopInstance(context.Background()); err == nil {
		t.Fatal("occupied data considered stopped")
	}
	env.Run = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("manager access denied")
	}
	if _, err = env.status(context.Background()); err == nil {
		t.Fatal("manager error swallowed")
	}
}
func TestSpecPlatformsPrivateDirectoryAndLock(t *testing.T) {
	dir := privateTempDir(t)
	first, err := lockDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := lockDataDir(dir); err == nil {
		second.Close()
		t.Fatal("second owner admitted")
	}
	first.Close()
	second, err := lockDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	second.Close()
	if runtime.GOOS == "windows" {
		// Windows protects the directory with a current-user DACL; POSIX chmod
		// does not describe its access policy.
		assertPrivatePlatformPath(t, dir, 0700)
		return
	}
	if err = os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if file, err := lockDataDir(dir); err == nil {
		file.Close()
		t.Fatal("public data directory accepted")
	}
	if mode, err := os.Stat(dir); err != nil || mode.Mode().Perm() != 0755 {
		t.Fatal("unexpected permission overwrite")
	}
}
func TestSpecServiceForeignPortOwnerIsNotVerified(t *testing.T) {
	env := fakePlatform(t, "darwin")
	hash, _ := fileSHA(env.Executable)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"pid":123,"build_id":"same"}`) }))
	defer server.Close()
	env.Listen = strings.TrimPrefix(server.URL, "http://")
	record := runtimePrivate{runtimeInstance{PID: 123, Executable: env.Executable, ConfigPath: env.ConfigPath, DataDir: env.DataDir, Listen: env.Listen, BuildID: "same", BinarySHA: hash}, "test"}
	_ = writePrivate(filepath.Join(env.DataDir, "runtime.json"), mustPlatformJSON(record))
	env.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "launchctl" {
			return nil, errServiceAbsent
		}
		if strings.Contains(strings.Join(args, " "), "-iTCP:") {
			return []byte("p456\n"), nil
		}
		return []byte("p123\n"), nil
	}
	status, err := env.status(context.Background())
	if err != nil || status.Verified || status.Running || status.PortOwnerPID == nil || *status.PortOwnerPID != 456 {
		t.Fatal(status, err)
	}
}

func TestSpecServiceInstallPreparesPrivateLogDirectory(t *testing.T) {
	env := fakePlatform(t, "darwin")
	if err := os.Remove(env.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := env.service(context.Background(), "install"); err != nil {
		t.Fatal(err)
	}
	assertPrivatePlatformPath(t, env.DataDir, 0700)
}

func TestSpecWindowsTaskQueryDistinguishesAbsentAndFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		err    error
		absent bool
	}{
		{"absent", `{"exists":false}`, nil, true},
		{"permission_error", "", errors.New("access denied"), false},
		{"malformed", `{}`, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := fakePlatform(t, "windows")
			env.Run = func(context.Context, string, ...string) ([]byte, error) { return []byte(tc.output), tc.err }
			registered, _, state, err := env.managerStatus(context.Background())
			if registered || err == nil || errors.Is(err, errServiceAbsent) != tc.absent {
				t.Fatalf("task query lost absence/error contract: %v %s %v", registered, state, err)
			}
		})
	}
}

func TestSpecWindowsOmittedRunLevelStillRequiresLeastPrivilege(t *testing.T) {
	for _, level := range []any{0, 1, nil} {
		env := fakePlatform(t, "windows")
		document, err := env.serviceDocument()
		if err != nil {
			t.Fatal(err)
		}
		if err := writePrivate(env.servicePath(), document); err != nil {
			t.Fatal(err)
		}
		exported := strings.Replace(windowsTaskText(document), "<RunLevel>LeastPrivilege</RunLevel>", "", 1)
		env.Run = func(context.Context, string, ...string) ([]byte, error) {
			return mustPlatformJSON(map[string]any{"exists": true, "document": exported, "state": 3, "run_level": level}), nil
		}
		registered, _, state, err := env.managerStatus(context.Background())
		if level == 0 {
			if err != nil || !registered || state != "ready" {
				t.Fatal("default LUA export rejected", state, err)
			}
		} else if err == nil || state != "conflict" {
			t.Fatal("unknown or elevated privilege accepted", state, err)
		}
	}
}
