package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func syntheticUpdateSwitch(t *testing.T, platform ...string) (platformEnvironment, updateJournal, string) {
	t.Helper()
	env, _, _, _, _ := syntheticRestoreFixture(t)
	if len(platform) > 0 {
		env.OS = platform[0]
	}
	var err error
	env.Executable, err = filepath.EvalSymlinks(env.Executable)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(env.DataDir, "gatt.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA journal_mode=WAL; PRAGMA user_version=2; CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE retained(value TEXT); INSERT INTO retained VALUES('keep')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = os.Mkdir(filepath.Join(env.DataDir, "secrets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filepath.Join(env.DataDir, "secrets", "credentials.json"), []byte(`{"synthetic":"fixture-only"}`)); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(env.DataDir, ".operations", "fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filepath.Join(env.DataDir, ".operations", "fixture", "private-reference"), []byte("local reference")); err != nil {
		t.Fatal(err)
	}
	packagePath, hash := packageFixture(t, env, nil)
	journalPath, err := env.prepareUpdate(packagePath, hash)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := readUpdateJournal(journalPath, env)
	if err != nil {
		t.Fatal(err)
	}
	env.Executable = journal.Updater
	return env, journal, journalPath
}

func TestSpecUpdateSwitchClosedReadinessAndRollback(t *testing.T) {
	for _, scenario := range []string{"success", "readiness-failure", "schema-change", "admission-open", "activation-unknown", "snapshot-rejection", "spawn-failure"} {
		t.Run(scenario, func(t *testing.T) {
			env, journal, filename := syntheticUpdateSwitch(t)
			activeBuild := journal.OldBuildID
			running, closed := true, false
			var owner *os.File
			var events []string
			record := runtimePrivate{}
			publish := func(build string) {
				var err error
				owner, err = lockDataDir(env.DataDir)
				if err != nil {
					t.Fatal(err)
				}
				hash, err := fileSHA(journal.Target)
				if err != nil {
					t.Fatal(err)
				}
				record = runtimePrivate{runtimeInstance: runtimeInstance{PID: 123, Executable: journal.Target, ConfigPath: env.ConfigPath, DataDir: env.DataDir, Listen: env.Listen, BuildID: build, BinarySHA: hash, StartedAt: time.Now()}, ControlToken: "synthetic-runtime"}
				if err = writePrivate(filepath.Join(env.DataDir, "runtime.json"), mustPlatformJSON(record)); err != nil {
					t.Fatal(err)
				}
			}
			publish(activeBuild)
			defer func() {
				if owner != nil {
					owner.Close()
				}
			}()
			if scenario == "snapshot-rejection" {
				if err := os.Symlink("secrets", filepath.Join(env.DataDir, ".response-cache")); err != nil {
					t.Fatal(err)
				}
			}
			env.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "launchctl" {
					return nil, errServiceAbsent
				}
				if name == "lsof" {
					for _, argument := range args {
						if argument == "-Fp" {
							return []byte("p123\n"), nil
						}
					}
				}
				return []byte("n" + journal.Target + "\n"), nil
			}
			env.Client = &http.Client{Transport: restoreTestTransport(func(r *http.Request) (*http.Response, error) {
				if !running {
					return nil, errors.New("synthetic instance stopped")
				}
				switch r.URL.Path {
				case "/healthz":
					return restoreTestResponse(200, `{"pid":123,"build_id":"`+activeBuild+`"}`), nil
				case "/readyz":
					if closed || activeBuild == journal.NewBuildID && scenario == "readiness-failure" {
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
					return restoreTestResponse(200, "{}"), nil
				case "/__cove/resume":
					events = append(events, "resume")
					return restoreTestResponse(200, "{}"), nil
				case "/__cove/stop":
					events = append(events, "stop-"+activeBuild)
					running = false
					owner.Close()
					owner = nil
					return restoreTestResponse(202, ""), nil
				case "/__cove/state":
					initialized := !(activeBuild == journal.NewBuildID && scenario == "readiness-failure")
					return restoreTestResponse(200, `{"admission_closed":`+map[bool]string{true: "true", false: "false"}[closed]+`,"initialized_ready":`+map[bool]string{true: "true", false: "false"}[initialized]+`}`), nil
				case "/__cove/activate":
					closed = false
					events = append(events, "activate")
					if scenario == "activation-unknown" {
						return restoreTestResponse(503, ""), nil
					}
					return restoreTestResponse(200, "{}"), nil
				}
				return restoreTestResponse(404, ""), nil
			})}
			spawn := func(_ context.Context, executable string, args []string, dir string) error {
				if executable != journal.Target || dir != env.DataDir || strings.Contains(strings.Join(args, " "), "restore-pointer") {
					t.Fatal("unexpected startup identity", args)
				}
				closed = strings.Contains(strings.Join(args, " "), "-admission-closed=true")
				if closed {
					if scenario == "spawn-failure" {
						return errors.New("synthetic spawn failure")
					}
					activeBuild = journal.NewBuildID
					events = append(events, "start-new-closed")
					if scenario == "schema-change" {
						db, err := sql.Open("sqlite3", filepath.Join(env.DataDir, "gatt.db"))
						if err != nil {
							t.Fatal(err)
						}
						_, err = db.Exec("PRAGMA user_version=3")
						db.Close()
						if err != nil {
							t.Fatal(err)
						}
					}
					if scenario == "admission-open" {
						closed = false
					}
				} else {
					activeBuild = journal.OldBuildID
					events = append(events, "start-old")
				}
				running = true
				publish(activeBuild)
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
			defer cancel()
			err := env.applyUpdateSwitch(ctx, filename, spawn)
			updated, readErr := readUpdateJournal(filename, env)
			if readErr != nil {
				t.Fatal(readErr)
			}
			targetSHA, _ := fileSHA(journal.Target)
			switch scenario {
			case "success":
				if err != nil || updated.Phase != "succeeded" || !running || closed || targetSHA != journal.NewBinarySHA || events[len(events)-1] != "activate" {
					t.Fatal(err, updated.Phase, events)
				}
				if err = env.applyUpdateSwitch(ctx, filename, spawn); err != nil {
					t.Fatal("verified completed switch failed", err)
				}
				closed = true
				if err = env.applyUpdateSwitch(ctx, filename, spawn); err == nil {
					t.Fatal("completed journal substituted for live activation")
				}
			case "readiness-failure", "spawn-failure":
				if err == nil || updated.Phase != "rolled_back" || !running || activeBuild != journal.OldBuildID || closed || targetSHA != journal.OldBinarySHA {
					t.Fatal(err, updated.Phase, events)
				}
			case "schema-change", "admission-open":
				if err == nil || updated.Phase != "rollback_blocked" || targetSHA != journal.NewBinarySHA {
					t.Fatal("unsafe rollback", err, updated.Phase, events)
				}
			case "activation-unknown":
				if err == nil || updated.Phase != "activation_uncertain" || !running || activeBuild != journal.NewBuildID || targetSHA != journal.NewBinarySHA {
					t.Fatal(err, updated.Phase, events)
				}
			case "snapshot-rejection":
				if err == nil || updated.Phase != "failed_before_stop" || targetSHA != journal.OldBinarySHA || !running || strings.Join(events, ",") != "drain,resume" {
					t.Fatal("snapshot failure stopped old", err, updated.Phase, events)
				}
			}
			if scenario != "snapshot-rejection" {
				if updated.Snapshot == "" {
					t.Fatal("missing snapshot reference")
				}
				if schema, err := readDataSchema(updated.Snapshot); err != nil || schema != 2 {
					t.Fatal("snapshot schema changed", schema, err)
				}
				data, err := os.ReadFile(filepath.Join(updated.Snapshot, "secrets", "credentials.json"))
				if err != nil || string(data) != `{"synthetic":"fixture-only"}` {
					t.Fatal("snapshot lost private refs", err)
				}
				data, err = os.ReadFile(filepath.Join(updated.Snapshot, ".operations", "fixture", "private-reference"))
				if err != nil || string(data) != "local reference" {
					t.Fatal("snapshot lost operation ref", err)
				}
			}
		})
	}
}

func TestSpecUpdateSwitchDelegatesOnlyVerifiedUpdater(t *testing.T) {
	env, journal, filename := syntheticUpdateSwitch(t)
	env.Executable = journal.Target
	called := false
	env.Run = func(_ context.Context, executable string, args ...string) ([]byte, error) {
		called = true
		if executable != journal.Updater || !strings.Contains(strings.Join(args, " "), "update apply --journal "+filename) {
			t.Fatal(executable, args)
		}
		return []byte("synthetic helper complete\n"), nil
	}
	if err := env.applyUpdateSwitch(context.Background(), filename, nil); err != nil || !called {
		t.Fatal(err, called)
	}
	called = false
	if err := os.WriteFile(journal.Updater, []byte("tampered"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := env.applyUpdateSwitch(context.Background(), filename, nil); err == nil || called {
		t.Fatal("unverified helper executed")
	}
}

func TestSpecUpdateSwitchRefusesServiceAndInterruptedPhase(t *testing.T) {
	for _, scenario := range []string{"service", "draining", "backed_up", "new_started_closed", "ready_closed"} {
		t.Run(scenario, func(t *testing.T) {
			env, journal, filename := syntheticUpdateSwitch(t)
			if scenario == "service" {
				target := updateTargetEnvironment(env, journal)
				document, err := target.serviceDocument()
				if err != nil {
					t.Fatal(err)
				}
				if err = writePrivate(target.servicePath(), document); err != nil {
					t.Fatal(err)
				}
			} else {
				journal.Phase = scenario
				if err := writePrivate(filename, mustPlatformJSON(journal)); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			env.Client = &http.Client{Transport: restoreTestTransport(func(*http.Request) (*http.Response, error) { called = true; return nil, errors.New("unexpected HTTP") })}
			if err := env.applyUpdateSwitch(context.Background(), filename, func(context.Context, string, []string, string) error { called = true; return nil }); err == nil || called {
				t.Fatal("unsafe stage executed", err, called)
			}
			if hash, _ := fileSHA(journal.Target); hash != journal.OldBinarySHA {
				t.Fatal("target modified")
			}
		})
	}
}

func TestSpecUpdateSwitchRefusesUninitializedSchemaBeforeDrain(t *testing.T) {
	env, journal, filename := syntheticUpdateSwitch(t)
	if err := os.Remove(filepath.Join(env.DataDir, "gatt.db")); err != nil {
		t.Fatal(err)
	}
	journal.OldSchema = 0
	if err := writePrivate(filename, mustPlatformJSON(journal)); err != nil {
		t.Fatal(err)
	}
	called := false
	env.Client = &http.Client{Transport: restoreTestTransport(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected HTTP")
	})}
	if err := env.applyUpdateSwitch(context.Background(), filename, func(context.Context, string, []string, string) error {
		called = true
		return nil
	}); err == nil || called {
		t.Fatal("schema initialization crossed stop boundary", err, called)
	}
	current, err := readUpdateJournal(filename, env)
	if err != nil || current.Phase != "prepared" {
		t.Fatal("refusal modified journal", err, current.Phase)
	}
}

func TestSpecUpdateSnapshotIncludesCommittedWALAndRejectsSpool(t *testing.T) {
	env, journal, _ := syntheticUpdateSwitch(t)
	db, err := sql.Open("sqlite3", filepath.Join(env.DataDir, "gatt.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; INSERT INTO retained VALUES('committed WAL')"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(env.DataDir, "gatt.db-wal")); err != nil {
		t.Fatal("fixture missing WAL", err)
	}
	snapshot, err := createUpdateSnapshot(context.Background(), journal)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := sql.Open("sqlite3", filepath.Join(snapshot, "gatt.db"))
	if err != nil {
		t.Fatal(err)
	}
	var value string
	err = copy.QueryRow("SELECT value FROM retained WHERE value='committed WAL'").Scan(&value)
	copy.Close()
	if err != nil || value != "committed WAL" {
		t.Fatal("committed WAL omitted", err)
	}
	if err = os.RemoveAll(snapshot); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(env.DataDir, ".resource-spool-fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = createUpdateSnapshot(context.Background(), journal); err == nil {
		t.Fatal("spool ignored")
	}
}

// This runs only a newly built synthetic Cove binary in this test's private
// temp data directory and ephemeral loopback port. Manager queries are mocked;
// actual PID/executable/port checks apply exclusively to that owned subprocess.
func TestSpecUpdateSwitchRealHelperSubprocess(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("native subprocess acceptance requires a supported build host")
	}
	if _, err := exec.LookPath("lsof"); runtime.GOOS != "windows" && err != nil {
		t.Skip("native identity tool lsof unavailable")
	}
	env, journal, filename := syntheticUpdateSwitch(t, runtime.GOOS)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	args := []string{"build"}
	if overlay := os.Getenv("COVE_UPDATE_TEST_OVERLAY"); overlay != "" {
		args = append(args, "-overlay", overlay)
	}
	args = append(args, "-ldflags", "-X gatt/internal/app.BuildID="+journal.NewBuildID, "-o", journal.NewBinary, "./cmd/gatt")
	buildCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	build := exec.CommandContext(buildCtx, "go", args...)
	build.Dir = repoRoot
	if err = os.Remove(journal.NewBinary); err != nil {
		t.Fatal(err)
	}
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("native candidate build: %v\n%s", err, output)
	}
	journal.NewBinarySHA, err = fileSHA(journal.NewBinary)
	if err != nil {
		t.Fatal(err)
	}
	if err = writePrivate(filename, mustPlatformJSON(journal)); err != nil {
		t.Fatal(err)
	}
	loggedOwnedPaths := false
	env.Run = func(ctx context.Context, executable string, args ...string) ([]byte, error) {
		if executable == "launchctl" {
			return nil, errServiceAbsent
		}
		if executable == "systemctl" {
			return []byte("LoadState=not-found\n"), nil
		}
		if executable == "powershell.exe" {
			raw, err := base64.StdEncoding.DecodeString(args[len(args)-1])
			if err != nil {
				return nil, err
			}
			units := make([]uint16, len(raw)/2)
			for i := range units {
				units[i] = binary.LittleEndian.Uint16(raw[i*2:])
			}
			if strings.Contains(string(utf16.Decode(units)), "Get-ScheduledTask") {
				return []byte(`{"exists":false}`), nil
			}
			return nativePlatformRunner(ctx, executable, args...)
		}
		if executable != "lsof" {
			return nil, errors.New("unexpected native command")
		}
		output, err := nativePlatformRunner(ctx, executable, args...)
		if !loggedOwnedPaths && len(args) > 0 && args[len(args)-1] == "-Fn" {
			loggedOwnedPaths = true
			for _, line := range strings.Split(string(output), "\n") {
				if strings.HasPrefix(line, "n") && strings.Contains(line, "TestSpecUpdateSwitchRealHelperSubprocess") {
					t.Logf("owned lsof path=%q; expected=%q", line[1:], journal.Target)
				}
			}
		}
		return output, err
	}
	checkedClosed := false
	env.Client = &http.Client{Timeout: 3 * time.Second, Transport: restoreTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/__cove/activate" {
			request, err := http.NewRequestWithContext(r.Context(), "POST", "http://"+env.Listen+"/v1/responses", strings.NewReader(`{"model":"synthetic-unconfigured"}`))
			if err != nil {
				return nil, err
			}
			response, err := http.DefaultTransport.RoundTrip(request)
			if err != nil {
				return nil, err
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 503 {
				return nil, errors.New("real child admitted before activation")
			}
			checkedClosed = true
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	var children []*exec.Cmd
	spawn := func(ctx context.Context, executable string, args []string, _ string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		child := exec.Command(executable, args...)
		child.Dir = filepath.Dir(executable)
		child.Stdout, child.Stderr = io.Discard, io.Discard
		if err := child.Start(); err != nil {
			return err
		}
		children = append(children, child)
		return nil
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		target := updateTargetEnvironment(env, journal)
		stopErr := target.stopInstance(cleanupCtx)
		for _, child := range children {
			if stopErr != nil {
				_ = child.Process.Signal(os.Interrupt)
			}
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			select {
			case <-done:
			case <-cleanupCtx.Done():
				_ = child.Process.Kill()
				<-done
				t.Error("owned test subprocess required forced cleanup")
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err = env.applyUpdateSwitch(ctx, filename, spawn); err != nil {
		target := updateTargetEnvironment(env, journal)
		if status, statusErr := target.status(context.Background()); statusErr == nil {
			t.Logf("synthetic runtime evidence: expected exe=%s config=%s data=%s listen=%s; instance=%+v; notice=%s", target.Executable, target.ConfigPath, target.DataDir, target.Listen, status.Instance, status.Notice)
			if status.Instance != nil {
				actual, owners, identityErr := target.liveIdentity(context.Background(), status.Instance.PID)
				t.Logf("owned process actual=%s owners=%v identity_error=%v", actual, owners, identityErr)
			}
		}
		t.Fatal(err)
	}
	updated, err := readUpdateJournal(filename, env)
	if err != nil || updated.Phase != "succeeded" || !checkedClosed {
		t.Fatal(err, updated.Phase, checkedClosed)
	}
	target := updateTargetEnvironment(env, journal)
	status, err := target.status(ctx)
	if err != nil || !status.Verified || !status.Ready || status.Instance == nil || status.Instance.BuildID != journal.NewBuildID {
		t.Fatal("real helper evidence", err, status.Notice)
	}
	record, err := readRuntime(env.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	closed, err := restoreControl(ctx, target, record, "state", "")
	if err != nil || closed {
		t.Fatal("real helper activation", err, closed)
	}
}
