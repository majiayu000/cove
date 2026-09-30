package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gatt/internal/app"
)

func TestSpecRestorePrivateControlsNoSelfOwner(t *testing.T) {
	record := &runtimePrivate{runtimeInstance: runtimeInstance{Listen: "127.0.0.1:1234"}, ControlToken: "synthetic-token"}
	var stopped, drains, resumes atomic.Int32
	var staged atomic.Bool
	staged.Store(true)
	handler := platformRestoreControlledHandler(http.NotFoundHandler(), record, platformRestoreControls{Stop: func() { stopped.Add(1) }, Drain: func(context.Context) (func(), error) { drains.Add(1); return func() { resumes.Add(1) }, nil }, SetStaged: func(v bool) { staged.Store(v) }, IsStaged: staged.Load})
	call := func(action, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://"+record.Listen+"/__cove/"+action, nil)
		r.RemoteAddr = "127.0.0.1:4567"
		r.Header.Set("X-Cove-Control", token)
		r.Header.Set("X-Cove-Drain", "synthetic-drain-owner")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call("drain", "wrong"); w.Code != 403 || drains.Load() != 0 {
		t.Fatal("unauthorized control", w.Code)
	}
	if w := call("state", record.ControlToken); w.Code != 200 || !strings.Contains(w.Body.String(), "true") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call("drain", record.ControlToken); w.Code != 200 || drains.Load() != 1 {
		t.Fatal(w.Code)
	}
	if w := call("drain", record.ControlToken); w.Code != 409 {
		t.Fatal("duplicate drain owned twice")
	}
	if w := call("activate", record.ControlToken); w.Code != 409 || !staged.Load() {
		t.Fatal("drain gate bypassed")
	}
	r := httptest.NewRequest("POST", "http://"+record.Listen+"/__cove/resume", nil)
	r.RemoteAddr = "127.0.0.1:4567"
	r.Header.Set("X-Cove-Control", record.ControlToken)
	r.Header.Set("X-Cove-Drain", "different-launcher")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 409 || resumes.Load() != 0 {
		t.Fatal("another launcher released the drain")
	}
	if w := call("resume", record.ControlToken); w.Code != 200 || resumes.Load() != 1 {
		t.Fatal(w.Code)
	}
	_ = call("resume", record.ControlToken)
	if resumes.Load() != 1 {
		t.Fatal("resume twice")
	}
	if w := call("activate", record.ControlToken); w.Code != 200 || staged.Load() {
		t.Fatal("activate missing")
	}
	if w := call("stop", record.ControlToken); w.Code != 202 || stopped.Load() != 1 {
		t.Fatal("stop missing")
	}
}
func TestSpecRestoreDrainTimeoutResumes(t *testing.T) {
	record := &runtimePrivate{runtimeInstance: runtimeInstance{Listen: "127.0.0.1:1234"}, ControlToken: "token"}
	resumed := false
	handler := platformRestoreControlledHandler(http.NotFoundHandler(), record, platformRestoreControls{Drain: func(ctx context.Context) (func(), error) {
		return func() { resumed = true }, errors.New("synthetic timeout")
	}})
	r := httptest.NewRequest("POST", "http://"+record.Listen+"/__cove/drain", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("X-Cove-Control", "token")
	r.Header.Set("X-Cove-Drain", "synthetic-drain-owner")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 503 || !resumed {
		t.Fatal("drain failure did not resume")
	}
}

type restoreTestTransport func(*http.Request) (*http.Response, error)

func (transport restoreTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}
func restoreTestResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}
func syntheticRestoreFixture(t *testing.T) (platformEnvironment, app.RestorePointer, app.RestorePointer, string, string) {
	t.Helper()
	env := fakePlatform(t, "darwin")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	env.Listen = listener.Addr().String()
	listener.Close()
	oldDir := env.DataDir
	nextDir := filepath.Join(env.Home, "新的 数据")
	if err = os.Mkdir(nextDir, 0700); err != nil {
		t.Fatal(err)
	}
	oldConfig := filepath.Join(oldDir, "config.json")
	newConfig := filepath.Join(nextDir, "config.json")
	c := app.Config{Listen: env.Listen, DataDir: oldDir, MaxBody: 1024, MaxResponse: 1024, MaxEvent: 1024, MaxConcurrent: 1, HeaderTimeout: 1, IdleTimeout: 1, TotalTimeout: 2, RetentionDays: 1, Codex: app.CodexConfig{BaseURL: "https://synthetic.invalid/v1", AuthBaseURL: "https://synthetic.invalid", ClientID: "fixture"}}
	if err = writePrivate(oldConfig, mustPlatformJSON(c)); err != nil {
		t.Fatal(err)
	}
	c.DataDir = nextDir
	if err = writePrivate(newConfig, mustPlatformJSON(c)); err != nil {
		t.Fatal(err)
	}
	env.ConfigPath = oldConfig
	old := app.RestorePointer{ConfigPath: oldConfig, DataDir: oldDir, BuildID: app.BuildID}
	next := app.RestorePointer{ConfigPath: newConfig, DataDir: nextDir, BuildID: app.BuildID}
	pointerPath := filepath.Join(env.Home, "runtime-pointer.json")
	if err = writePrivate(pointerPath, mustPlatformJSON(old)); err != nil {
		t.Fatal(err)
	}
	_, hash, err := app.ReadRestorePointer(pointerPath)
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(env.Home, "switch-journal.json")
	if _, err = app.PrepareRestoreSwitch(journalPath, pointerPath, hash, next); err != nil {
		t.Fatal(err)
	}
	return env, old, next, pointerPath, journalPath
}
func TestSpecRestoreLauncherSyntheticStartReadyAndRollback(t *testing.T) {
	for _, failNew := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "ready-failure"}[failNew], func(t *testing.T) {
			env, old, next, pointerPath, journalPath := syntheticRestoreFixture(t)
			active := old
			running := true
			closed := false
			var events []string
			record := runtimePrivate{}
			storeRuntime := func(pointer app.RestorePointer) {
				hash, _ := fileSHA(env.Executable)
				record = runtimePrivate{runtimeInstance: runtimeInstance{PID: 123, Executable: env.Executable, ConfigPath: pointer.ConfigPath, DataDir: pointer.DataDir, Listen: env.Listen, BuildID: app.BuildID, BinarySHA: hash, StartedAt: time.Now()}, ControlToken: "synthetic-" + filepath.Base(pointer.DataDir)}
				if err := writePrivate(filepath.Join(pointer.DataDir, "runtime.json"), mustPlatformJSON(record)); err != nil {
					t.Fatal(err)
				}
			}
			storeRuntime(old)
			env.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "launchctl" {
					return nil, errServiceAbsent
				}
				if name == "lsof" && strings.Contains(strings.Join(args, " "), "-Fp") {
					return []byte("p123\n"), nil
				}
				return []byte("n" + env.Executable + "\n"), nil
			}
			env.Client = &http.Client{Timeout: time.Second, Transport: restoreTestTransport(func(r *http.Request) (*http.Response, error) {
				if !running {
					return nil, errors.New("synthetic process absent")
				}
				switch r.URL.Path {
				case "/healthz":
					return restoreTestResponse(200, `{"pid":123,"build_id":"`+app.BuildID+`"}`), nil
				case "/readyz":
					if failNew && active == next {
						return restoreTestResponse(503, ""), nil
					}
					return restoreTestResponse(200, "{}"), nil
				}
				if r.Header.Get("X-Cove-Control") != record.ControlToken {
					return restoreTestResponse(403, ""), nil
				}
				switch r.URL.Path {
				case "/__cove/drain":
					events = append(events, "drain")
					return restoreTestResponse(200, `{"drained":true}`), nil
				case "/__cove/resume":
					events = append(events, "resume")
					return restoreTestResponse(200, "{}"), nil
				case "/__cove/stop":
					events = append(events, "stop-"+filepath.Base(active.DataDir))
					running = false
					return restoreTestResponse(202, ""), nil
				case "/__cove/state":
					return restoreTestResponse(200, `{"admission_closed":`+map[bool]string{true: "true", false: "false"}[closed]+`}`), nil
				case "/__cove/activate":
					closed = false
					events = append(events, "activate")
					return restoreTestResponse(200, "{}"), nil
				}
				return restoreTestResponse(404, ""), nil
			})}
			spawn := func(_ context.Context, binary string, args []string, dir string) error {
				if binary != env.Executable || !strings.Contains(strings.Join(args, " "), "-restore-pointer "+pointerPath) {
					t.Fatal("not trusted controlled spawn", args)
				}
				closed = strings.Contains(strings.Join(args, " "), "-admission-closed=true")
				if dir == next.DataDir {
					active = next
					if !closed {
						t.Fatal("new process admitted before verification")
					}
				} else {
					active = old
				}
				events = append(events, "start-"+filepath.Base(dir))
				running = true
				storeRuntime(active)
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := env.applyRestore(ctx, journalPath, spawn)
			selected, _, readErr := app.ReadRestorePointer(pointerPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if failNew {
				if err == nil || selected != old || active != old || !running || closed {
					t.Fatal("old instance not restored", err, selected, events)
				}
			} else {
				if err != nil || selected != next || active != next || !running || closed {
					t.Fatal("new readiness/activation failed", err, selected, events)
				}
				if events[len(events)-1] != "activate" {
					t.Fatal("did not activate last", events)
				}
				if err = env.applyRestore(ctx, journalPath, func(context.Context, string, []string, string) error {
					t.Fatal("completed switch restarted")
					return nil
				}); err != nil {
					t.Fatal("verified completed switch failed", err)
				}
				closed = true
				if err = env.applyRestore(ctx, journalPath, spawn); err == nil {
					t.Fatal("completed journal reported current activation without evidence")
				}
			}
		})
	}
}
func TestSpecRestoreLauncherRefusesRegistrationBeforeStop(t *testing.T) {
	env, old, _, _, journalPath := syntheticRestoreFixture(t)
	document, _ := env.serviceDocument()
	if err := writePrivate(env.servicePath(), document); err != nil {
		t.Fatal(err)
	}
	called := false
	env.Client = &http.Client{Transport: restoreTestTransport(func(*http.Request) (*http.Response, error) { called = true; return nil, errors.New("must not stop") })}
	if err := env.applyRestore(context.Background(), journalPath, func(context.Context, string, []string, string) error { called = true; return nil }); err == nil || !strings.Contains(err.Error(), "uninstall") {
		t.Fatal("registered service accepted", err)
	}
	if called {
		t.Fatal("performed side effect before registration refusal")
	}
	pointer, _, err := app.ReadRestorePointer(filepath.Join(env.Home, "runtime-pointer.json"))
	if err != nil || pointer != old {
		t.Fatal("pointer changed")
	}
}
func TestSpecRestoreStartupResolution(t *testing.T) {
	env, old, _, pointerPath, _ := syntheticRestoreFixture(t)
	config, dir, err := resolveRestoreStartup(pointerPath, "ignored", "ignored")
	if err != nil || config != old.ConfigPath || dir != old.DataDir {
		t.Fatal(config, dir, err)
	}
	config, dir, err = resolveRestoreStartup("", env.ConfigPath, env.DataDir)
	if err != nil || !reflect.DeepEqual([]string{config, dir}, []string{env.ConfigPath, env.DataDir}) {
		t.Fatal("changed ordinary startup")
	}
}
func TestSpecRestoreControlTokenNotInStatusOrOutput(t *testing.T) {
	record := runtimePrivate{runtimeInstance: runtimeInstance{PID: 1}, ControlToken: "SYNTHETIC_CONTROL_SECRET"}
	raw, _ := json.Marshal(record.runtimeInstance)
	if bytes.Contains(raw, []byte(record.ControlToken)) {
		t.Fatal("control token exported")
	}
}

func TestSpecRestoreDrainLostResponseResumesOnlyOwnGate(t *testing.T) {
	env, old, _, pointerPath, _ := syntheticRestoreFixture(t)
	hash, err := fileSHA(env.Executable)
	if err != nil {
		t.Fatal(err)
	}
	record := runtimePrivate{runtimeInstance: runtimeInstance{PID: 123, Executable: env.Executable, ConfigPath: old.ConfigPath, DataDir: old.DataDir, Listen: env.Listen, BuildID: app.BuildID, BinarySHA: hash}, ControlToken: "synthetic-control"}
	if err = writePrivate(filepath.Join(old.DataDir, "runtime.json"), mustPlatformJSON(record)); err != nil {
		t.Fatal(err)
	}
	env.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "launchctl" {
			return nil, errServiceAbsent
		}
		if strings.Contains(strings.Join(args, " "), "-Fp") {
			return []byte("p123\n"), nil
		}
		return []byte("n" + env.Executable + "\n"), nil
	}
	var owner string
	var resumed bool
	env.Client = &http.Client{Transport: restoreTestTransport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/healthz":
			return restoreTestResponse(200, `{"pid":123,"build_id":"`+app.BuildID+`"}`), nil
		case "/readyz":
			return restoreTestResponse(200, "{}"), nil
		case "/__cove/drain":
			owner = r.Header.Get("X-Cove-Drain")
			if owner == "" {
				t.Fatal("missing drain owner")
			}
			return nil, errors.New("synthetic response lost after gate acquisition")
		case "/__cove/resume":
			if r.Header.Get("X-Cove-Drain") != owner {
				t.Fatal("resumed another owner")
			}
			resumed = true
			return restoreTestResponse(503, "synthetic resume failure"), nil
		}
		return restoreTestResponse(404, ""), nil
	})}
	launcher := &platformRestoreLauncher{Environment: env, PointerPath: pointerPath, BinarySHA: hash, closed: map[string]bool{}}
	_, err = launcher.drain(context.Background(), old)
	if err == nil || !resumed || launcher.resumeError == nil || !strings.Contains(err.Error(), "503") {
		t.Fatal("lost response/resume failure hidden", err, resumed, launcher.resumeError)
	}
}
