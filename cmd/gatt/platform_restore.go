package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"gatt/internal/app"
)

type platformRestoreControls struct {
	Stop      func()
	Drain     func(context.Context) (func(), error)
	SetStaged func(bool)
	IsStaged  func() bool
	Ready     func(context.Context) error
}

// This wrapper sits outside App.ServeHTTP so the drain request never waits on
// its own HTTP owner. All controls share one runtime-private token boundary.
func platformRestoreControlledHandler(next http.Handler, record *runtimePrivate, hooks platformRestoreControls) http.Handler {
	var mu sync.Mutex
	var resume func()
	var drainOwner string
	draining := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/__cove/") {
			next.ServeHTTP(w, r)
			return
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if record.ControlToken == "" || r.Method != "POST" || r.Host != record.Listen || err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Cove-Control")), []byte(record.ControlToken)) != 1 {
			http.Error(w, "forbidden", 403)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		switch r.URL.Path {
		case "/__cove/stop":
			if hooks.Stop == nil {
				http.Error(w, "shutdown unavailable", 503)
				return
			}
			w.WriteHeader(202)
			_, _ = w.Write([]byte("shutdown requested\n"))
			hooks.Stop()
		case "/__cove/drain":
			if hooks.Drain == nil {
				http.Error(w, "drain unavailable", 503)
				return
			}
			owner := r.Header.Get("X-Cove-Drain")
			if owner == "" || len(owner) > 128 {
				http.Error(w, "drain owner required", 400)
				return
			}
			mu.Lock()
			if draining || resume != nil {
				mu.Unlock()
				http.Error(w, "drain already owned", 409)
				return
			}
			draining = true
			drainOwner = owner
			mu.Unlock()
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			release, err := hooks.Drain(ctx)
			cancel()
			if err == nil && release == nil {
				err = errors.New("drain release unavailable")
			}
			mu.Lock()
			draining = false
			if err == nil {
				resume = release
			} else {
				drainOwner = ""
			}
			mu.Unlock()
			if err != nil {
				if release != nil {
					release()
				}
				http.Error(w, "drain_timeout", 503)
				return
			}
			_, _ = w.Write([]byte("{\"drained\":true}\n"))
		case "/__cove/resume":
			mu.Lock()
			if draining || drainOwner != "" && r.Header.Get("X-Cove-Drain") != drainOwner {
				mu.Unlock()
				http.Error(w, "drain owner not released", 409)
				return
			}
			release := resume
			resume = nil
			drainOwner = ""
			mu.Unlock()
			if release != nil {
				release()
			}
			w.WriteHeader(200)
		case "/__cove/state":
			if hooks.IsStaged == nil {
				http.Error(w, "admission state unavailable", 503)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			initialized := false
			if hooks.Ready != nil {
				if err := hooks.Ready(r.Context()); err != nil {
					http.Error(w, "initialization not ready", 503)
					return
				}
				initialized = true
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"admission_closed": hooks.IsStaged(), "initialized_ready": initialized})
		case "/__cove/activate":
			if hooks.SetStaged == nil || hooks.IsStaged == nil {
				http.Error(w, "activation unavailable", 503)
				return
			}
			mu.Lock()
			busy := draining || resume != nil
			if busy {
				mu.Unlock()
				http.Error(w, "drain owns admission", 409)
				return
			}
			hooks.SetStaged(false)
			closed := hooks.IsStaged()
			mu.Unlock()
			if closed {
				http.Error(w, "activation not confirmed", 503)
				return
			}
			w.WriteHeader(200)
		default:
			http.NotFound(w, r)
		}
	})
}

// Invoke immediately after flag parsing and before LoadConfigWithDataDir.
// The explicit pointer is authoritative; its selected config/data paths are
// printed by status and verified by the launcher, rather than silently merged.
func resolveRestoreStartup(pointerPath, configPath, dataDir string) (string, string, error) {
	if pointerPath == "" {
		return configPath, dataDir, nil
	}
	pointer, _, err := app.ReadRestorePointer(pointerPath)
	if err != nil {
		return "", "", err
	}
	if pointer.BuildID != app.BuildID {
		return "", "", errors.New("恢复指针 build 与当前可信 binary 不符")
	}
	return pointer.ConfigPath, pointer.DataDir, nil
}

type platformRestoreSpawn func(context.Context, string, []string, string) error

func nativePlatformRestoreSpawn(ctx context.Context, binary string, args []string, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	logPath := filepath.Join(dir, "restore-launch.log")
	if info, err := os.Lstat(logPath); err == nil && !info.Mode().IsRegular() {
		return errors.New("恢复启动日志不是普通文件")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	if err = log.Chmod(0600); err != nil {
		return err
	}
	command := exec.Command(binary, args...)
	command.Dir = filepath.Dir(binary)
	command.Stdout, command.Stderr = log, log
	if err = command.Start(); err != nil {
		return fmt.Errorf("可信 binary 启动失败: %w", err)
	}
	return command.Process.Release()
}

type platformRestoreLauncher struct {
	Environment platformEnvironment
	Spawn       platformRestoreSpawn
	PointerPath string
	BinarySHA   string
	closed      map[string]bool
	resumeError error
}

func (launcher *platformRestoreLauncher) environmentFor(pointer app.RestorePointer) (platformEnvironment, error) {
	env := launcher.Environment
	config, err := app.LoadConfigWithDataDir(pointer.ConfigPath, pointer.DataDir)
	if err != nil {
		return env, err
	}
	env.ConfigPath, env.DataDir, env.Listen = pointer.ConfigPath, pointer.DataDir, config.Listen
	return env, nil
}
func (launcher *platformRestoreLauncher) noService(ctx context.Context, pointer app.RestorePointer) error {
	env, err := launcher.environmentFor(pointer)
	if err != nil {
		return err
	}
	registered, _, _, err := env.managerStatus(ctx)
	if err != nil && !errors.Is(err, errServiceAbsent) {
		return err
	}
	if registered {
		return errors.New("仍有用户服务注册；请先 service uninstall（保留数据），再 restore apply，成功后按新指针重新 service install。启动器没有停止或覆盖注册")
	}
	return nil
}
func restoreStopped(env platformEnvironment) error {
	lock, err := lockDataDir(env.DataDir)
	if err != nil {
		return fmt.Errorf("恢复目录仍有写 owner/实例: %w", err)
	}
	defer lock.Close()
	listener, err := net.Listen("tcp", env.Listen)
	if err != nil {
		return errors.New("恢复监听端口仍被占用；拒绝判定停止或覆盖其他实例")
	}
	return listener.Close()
}
func (launcher *platformRestoreLauncher) verified(ctx context.Context, pointer app.RestorePointer) (platformEnvironment, runtimePrivate, platformStatus, error) {
	env, err := launcher.environmentFor(pointer)
	if err != nil {
		return env, runtimePrivate{}, platformStatus{}, err
	}
	status, err := env.status(ctx)
	if err != nil {
		return env, runtimePrivate{}, status, err
	}
	if !status.Verified || status.Instance == nil || status.Instance.BuildID != pointer.BuildID {
		return env, runtimePrivate{}, status, errors.New("恢复目标的真实 PID/端口/binary/config/data/build 证据不一致")
	}
	record, err := readRuntime(pointer.DataDir)
	if err != nil || record.ControlToken == "" || record.runtimeInstance != *status.Instance {
		return env, record, status, errors.New("目标私有运行记录不可用或已改变")
	}
	return env, record, status, nil
}
func restoreControl(ctx context.Context, env platformEnvironment, record runtimePrivate, action, owner string) (bool, error) {
	client := *env.Client
	client.Timeout = 35 * time.Second
	request, err := http.NewRequestWithContext(ctx, "POST", "http://"+record.Listen+"/__cove/"+action, nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("X-Cove-Control", record.ControlToken)
	if owner != "" {
		request.Header.Set("X-Cove-Drain", owner)
	}
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return false, fmt.Errorf("私有 %s 控制未确认（HTTP %d）", action, response.StatusCode)
	}
	if action == "state" {
		var state struct {
			Closed *bool `json:"admission_closed"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&state) != nil || state.Closed == nil {
			return false, errors.New("准入门禁证据缺失")
		}
		return *state.Closed, nil
	}
	return false, nil
}
func (launcher *platformRestoreLauncher) drain(ctx context.Context, pointer app.RestorePointer) (func(), error) {
	if err := launcher.noService(ctx, pointer); err != nil {
		return nil, err
	}
	env, record, _, err := launcher.verified(ctx, pointer)
	if err != nil {
		if stopErr := restoreStopped(env); stopErr == nil {
			return func() {}, nil
		}
		return nil, err
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return nil, err
	}
	owner := hex.EncodeToString(random)
	resume := func() {
		resumeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, launcher.resumeError = restoreControl(resumeCtx, env, record, "resume", owner)
	}
	if _, err = restoreControl(ctx, env, record, "drain", owner); err != nil {
		// The response may have been lost after the server acquired the gate.
		// This nonce can release only this launcher's drain, never another one.
		resume()
		return nil, errors.Join(err, launcher.resumeError)
	}
	return resume, nil
}
func (launcher *platformRestoreLauncher) stop(ctx context.Context, pointer app.RestorePointer) error {
	env, err := launcher.environmentFor(pointer)
	if err != nil {
		return err
	}
	if err = launcher.noService(ctx, pointer); err != nil {
		return err
	}
	if err = env.stopInstance(ctx); err != nil {
		return err
	}
	return restoreStopped(env)
}
func (launcher *platformRestoreLauncher) start(ctx context.Context, pointer app.RestorePointer, closed bool) error {
	env, err := launcher.environmentFor(pointer)
	if err != nil {
		return err
	}
	if err = launcher.noService(ctx, pointer); err != nil {
		return err
	}
	if pointer.BuildID != app.BuildID {
		return errors.New("拒绝启动未经当前可信 binary 证实的 build")
	}
	hash, err := fileSHA(env.Executable)
	if err != nil || hash != launcher.BinarySHA {
		return errors.New("可信启动 binary 自 preflight 后已改变")
	}
	if err = restoreStopped(env); err != nil {
		return err
	}
	args := []string{"-config", pointer.ConfigPath, "-data-dir", pointer.DataDir, "-background", "-admission-closed=" + fmt.Sprint(closed)}
	if launcher.PointerPath != "" {
		args = append(args, "-restore-pointer", launcher.PointerPath)
	}
	args = append(args, "serve")
	launcher.closed[pointer.DataDir] = closed
	return launcher.Spawn(ctx, env.Executable, args, env.DataDir)
}

type restorePrivateState struct {
	AdmissionClosed  bool `json:"admission_closed"`
	InitializedReady bool `json:"initialized_ready"`
}

func restoreControlState(ctx context.Context, env platformEnvironment, record runtimePrivate) (restorePrivateState, error) {
	var state restorePrivateState
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(c, "POST", "http://"+env.Listen+"/__cove/state", nil)
	if err != nil {
		return state, err
	}
	req.Header.Set("X-Cove-Control", record.ControlToken)
	client := *env.Client
	client.Timeout = 5 * time.Second
	resp, err := client.Do(req)
	if err != nil {
		return state, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return state, errors.New("受控初始化检查失败")
	}
	var wire struct {
		AdmissionClosed  *bool `json:"admission_closed"`
		InitializedReady bool  `json:"initialized_ready"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&wire); err != nil {
		return state, err
	}
	if wire.AdmissionClosed == nil {
		return state, errors.New("受控状态缺少准入字段")
	}
	state.AdmissionClosed, state.InitializedReady = *wire.AdmissionClosed, wire.InitializedReady
	return state, nil
}
func (launcher *platformRestoreLauncher) ready(ctx context.Context, pointer app.RestorePointer) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastError error
	for {
		env, record, status, err := launcher.verified(ctx, pointer)
		if err == nil {
			state, stateErr := restoreControlState(ctx, env, record)
			if stateErr == nil && state.AdmissionClosed == launcher.closed[pointer.DataDir] && (status.Ready || launcher.closed[pointer.DataDir] && state.InitializedReady) {
				return nil
			}
			if stateErr != nil {
				err = stateErr
			} else {
				err = errors.New("目标初始化或准入状态不符，不能认为安全就绪")
			}
		}
		lastError = err
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), lastError)
		case <-ticker.C:
		}
	}
}
func (launcher *platformRestoreLauncher) activate(ctx context.Context, pointer app.RestorePointer) error {
	env, record, _, err := launcher.verified(ctx, pointer)
	if err != nil {
		return err
	}
	if _, err = restoreControl(ctx, env, record, "activate", ""); err != nil {
		return err
	}
	closed, err := restoreControl(ctx, env, record, "state", "")
	if err != nil {
		return err
	}
	if closed {
		return errors.New("准入开启结果未确认")
	}
	launcher.closed[pointer.DataDir] = false
	return nil
}
func (env platformEnvironment) applyRestore(ctx context.Context, journalPath string, spawn platformRestoreSpawn) error {
	info, err := os.Lstat(journalPath)
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return errors.New("恢复 journal 必须是私有普通文件")
	}
	file, err := os.Open(journalPath)
	if err != nil {
		return err
	}
	var journal app.RestoreSwitchJournal
	decoder := json.NewDecoder(io.LimitReader(file, 65536))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&journal)
	var extra any
	if err == nil && decoder.Decode(&extra) != io.EOF {
		err = errors.New("恢复 journal 有多余数据")
	}
	file.Close()
	if err != nil {
		return errors.New("恢复 journal 无效")
	}
	if journal.Old.BuildID != app.BuildID || journal.New.BuildID != app.BuildID {
		return errors.New("恢复仅使用当前可信 binary；build 不符")
	}
	hash, err := fileSHA(env.Executable)
	if err != nil {
		return err
	}
	launcher := &platformRestoreLauncher{Environment: env, Spawn: spawn, PointerPath: journal.PointerPath, BinarySHA: hash, closed: map[string]bool{journal.Old.DataDir: false}}
	if journal.Phase == "succeeded" {
		pointer, _, readErr := app.ReadRestorePointer(journal.PointerPath)
		if readErr != nil || pointer != journal.New {
			return errors.New("已完成 journal 的当前指针不符，请检查状态")
		}
		liveEnv, record, status, verifyErr := launcher.verified(ctx, journal.New)
		if verifyErr != nil {
			return verifyErr
		}
		if !status.Ready {
			return errors.New("journal 已完成，但当前实例未就绪；请检查 status")
		}
		closed, stateErr := restoreControl(ctx, liveEnv, record, "state", "")
		if stateErr != nil {
			return stateErr
		}
		if closed {
			return errors.New("journal 已完成，但当前实例仍关闭准入；请检查状态")
		}
		return nil
	}
	// Preflight both registration identities before any drain/stop side effect.
	if err = launcher.noService(ctx, journal.Old); err != nil {
		return err
	}
	if err = launcher.noService(ctx, journal.New); err != nil {
		return err
	}
	err = app.ApplyRestoreSwitch(ctx, journalPath, app.RestoreSwitchHooks{Drain: launcher.drain, Stop: launcher.stop, Start: launcher.start, Ready: launcher.ready, Activate: launcher.activate})
	if launcher.resumeError != nil {
		return errors.Join(err, fmt.Errorf("旧实例准入恢复未确认: %w", launcher.resumeError))
	}
	return err
}

// Root routes 'restore' here before opening listener/storage. Apply is a separate
// launcher process and never an admin HTTP operation waiting to stop itself.
func (env platformEnvironment) restoreCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("用法：restore init|prepare|apply --pointer FILE --journal FILE --target-config FILE --target-data-dir DIR")
	}
	flags := flag.NewFlagSet("restore "+args[0], flag.ContinueOnError)
	flags.SetOutput(env.Output)
	pointerPath := flags.String("pointer", "", "当前用户私有启动指针绝对路径")
	journalPath := flags.String("journal", "", "恢复 journal 绝对路径")
	targetConfig := flags.String("target-config", "", "已准备恢复目录的配置")
	targetData := flags.String("target-data-dir", "", "已准备恢复目录")
	expected := flags.String("expected-pointer-hash", "", "已确认旧指针 hash")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("restore 不接受额外参数")
	}
	switch args[0] {
	case "init":
		if !filepath.IsAbs(*pointerPath) {
			return errors.New("--pointer 必须绝对路径")
		}
		current := app.RestorePointer{ConfigPath: env.ConfigPath, DataDir: env.DataDir, BuildID: app.BuildID}
		if prior, hash, err := app.ReadRestorePointer(*pointerPath); err == nil {
			if prior != current {
				return errors.New("已有不同启动指针，拒绝覆盖")
			}
			_, err = env.Output.Write(mustPlatformJSON(map[string]string{"pointer": *pointerPath, "hash": hash}))
			return err
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := writePrivate(*pointerPath, mustPlatformJSON(current)); err != nil {
			return err
		}
		_, hash, err := app.ReadRestorePointer(*pointerPath)
		if err != nil {
			return err
		}
		_, err = env.Output.Write(mustPlatformJSON(map[string]string{"pointer": *pointerPath, "hash": hash}))
		return err
	case "prepare":
		if *journalPath == "" || *pointerPath == "" || *targetConfig == "" || *targetData == "" || *expected == "" {
			return errors.New("prepare 需 pointer/journal/target-config/target-data-dir/expected-pointer-hash")
		}
		config, err := app.LoadConfigWithDataDir(*targetConfig, *targetData)
		if err != nil {
			return err
		}
		next := app.RestorePointer{ConfigPath: *targetConfig, DataDir: config.DataDir, BuildID: app.BuildID}
		journal, err := app.PrepareRestoreSwitch(*journalPath, *pointerPath, *expected, next)
		if err != nil {
			return err
		}
		_, err = env.Output.Write(mustPlatformJSON(journal))
		return err
	case "apply":
		if *journalPath == "" {
			return errors.New("apply 缺 --journal")
		}
		if err := env.applyRestore(ctx, *journalPath, nativePlatformRestoreSpawn); err != nil {
			return err
		}
		_, err := env.Output.Write([]byte("恢复切换已通过 readiness 并开放准入；两个数据目录均保留。当前是独立后台进程，可按新指针明确重新安装用户服务。\n"))
		return err
	default:
		return errors.New("不支持此 restore 动作")
	}
}
