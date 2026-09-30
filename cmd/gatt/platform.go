package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"gatt/internal/app"
)

const serviceLabel = "com.cove.gatt"

var errServiceAbsent = errors.New("service is not registered")

type platformRunner func(context.Context, string, ...string) ([]byte, error)
type platformEnvironment struct {
	OS, Arch, Home, UserID, Executable, ConfigPath, DataDir, Listen string
	Run                                                             platformRunner
	Client                                                          *http.Client
	Output                                                          io.Writer
}

func nativePlatformRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	output, err := cmd.CombinedOutput()
	if err != nil {
		text := strings.ToLower(string(output))
		if strings.Contains(text, "could not find service") || strings.Contains(text, "the system cannot find the file specified") {
			return nil, errServiceAbsent
		}
		return nil, fmt.Errorf("%s 执行失败: %w", name, err)
	}
	return output, nil
}
func platformEnv(configPath string, c app.Config) (platformEnvironment, error) {
	exe, err := os.Executable()
	if err != nil {
		return platformEnvironment{}, err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return platformEnvironment{}, err
	}
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		return platformEnvironment{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return platformEnvironment{}, err
	}
	current, err := user.Current()
	if err != nil {
		return platformEnvironment{}, err
	}
	return platformEnvironment{runtime.GOOS, runtime.GOARCH, home, current.Uid, exe, configPath, c.DataDir, c.Listen, nativePlatformRunner, &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, os.Stdout}, nil
}
func platformDefaultDataDir(goos, home, xdg, local string) (string, error) {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Cove"), nil
	case "linux":
		if xdg == "" {
			xdg = filepath.Join(home, ".local", "share")
		}
		return filepath.Join(xdg, "Cove"), nil
	case "windows":
		if local == "" {
			return "", fmt.Errorf("LOCALAPPDATA 未设置，无法确定用户数据目录")
		}
		return filepath.Join(local, "Cove"), nil
	}
	return "", fmt.Errorf("平台不受支持")
}
func writePrivate(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("目标不是普通文件")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".cove-write-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(temporary, path); err != nil {
		return err
	}
	return syncPlatformDirectory(filepath.Dir(path))
}
func fileSHA(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func browserCommand(goos, address string) (string, []string, error) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", nil, fmt.Errorf("管理地址必须是本机 HTTP 地址")
	}
	host, _, err := net.SplitHostPort(parsed.Host)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return "", nil, fmt.Errorf("管理地址必须是 loopback 地址")
	}
	switch goos {
	case "darwin":
		return "open", []string{address}, nil
	case "linux":
		return "xdg-open", []string{address}, nil
	case "windows":
		return "rundll32.exe", []string{"url.dll,FileProtocolHandler", address}, nil
	}
	return "", nil, fmt.Errorf("此平台没有浏览器启动实现")
}
func openBrowser(address string) error {
	name, args, err := browserCommand(runtime.GOOS, address)
	if err != nil {
		return err
	}
	if err = exec.Command(name, args...).Run(); err != nil {
		return fmt.Errorf("浏览器无法打开，请检查默认浏览器: %w", err)
	}
	fmt.Println("已在浏览器打开 Cove 管理界面。")
	return nil
}

type runtimeInstance struct {
	PID        int       `json:"pid"`
	Executable string    `json:"executable"`
	ConfigPath string    `json:"config_path"`
	DataDir    string    `json:"data_dir"`
	Listen     string    `json:"listen"`
	BuildID    string    `json:"build_id"`
	Version    string    `json:"version"`
	BinarySHA  string    `json:"binary_sha256"`
	StartedAt  time.Time `json:"started_at"`
	LaunchMode string    `json:"launch_mode"`
}
type runtimePrivate struct {
	runtimeInstance
	ControlToken string `json:"control_token"`
}

func publishRuntime(configPath string, c app.Config, background bool) (*runtimePrivate, error) {
	env, err := platformEnv(configPath, c)
	if err != nil {
		return nil, err
	}
	hash, err := fileSHA(env.Executable)
	if err != nil {
		return nil, err
	}
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		return nil, err
	}
	mode := "interactive"
	if background {
		mode = "user-service"
	}
	record := &runtimePrivate{runtimeInstance{os.Getpid(), env.Executable, env.ConfigPath, env.DataDir, env.Listen, app.BuildID, app.Version, hash, time.Now().UTC(), mode}, hex.EncodeToString(random)}
	if err = writePrivate(filepath.Join(c.DataDir, "runtime.json"), mustPlatformJSON(record)); err != nil {
		return nil, err
	}
	return record, nil
}
func mustPlatformJSON(v any) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(data, '\n')
}
func platformControlledHandler(next http.Handler, record *runtimePrivate, stop func()) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/__cove/stop" {
			next.ServeHTTP(w, r)
			return
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if record.ControlToken == "" || r.Method != "POST" || r.Host != record.Listen || err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Cove-Control")), []byte(record.ControlToken)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("shutdown requested\n"))
		stop()
	})
}
func readRuntime(dir string) (runtimePrivate, error) {
	path := filepath.Join(dir, "runtime.json")
	info, err := os.Lstat(path)
	if err != nil {
		return runtimePrivate{}, err
	}
	if !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return runtimePrivate{}, fmt.Errorf("运行实例记录不是私有普通文件")
	}
	file, err := os.Open(path)
	if err != nil {
		return runtimePrivate{}, err
	}
	defer file.Close()
	var record runtimePrivate
	err = json.NewDecoder(io.LimitReader(file, 65536)).Decode(&record)
	return record, err
}

type platformStatus struct {
	OS                 string           `json:"os"`
	ExpectedExecutable string           `json:"expected_executable"`
	ExpectedConfig     string           `json:"expected_config"`
	DataDir            string           `json:"data_dir"`
	Listen             string           `json:"listen"`
	Registered         bool             `json:"registered"`
	ManagerState       string           `json:"manager_state"`
	ManagerPID         int              `json:"manager_pid"`
	Instance           *runtimeInstance `json:"instance,omitempty"`
	Running            bool             `json:"running"`
	Ready              bool             `json:"ready"`
	Verified           bool             `json:"verified"`
	PortOwnerPID       *int             `json:"port_owner_pid,omitempty"`
	LogPath            string           `json:"log_path,omitempty"`
	LogHint            string           `json:"log_hint,omitempty"`
	Notice             string           `json:"notice"`
}

func (env platformEnvironment) status(ctx context.Context) (platformStatus, error) {
	status := platformStatus{OS: env.OS, ExpectedExecutable: env.Executable, ExpectedConfig: env.ConfigPath, DataDir: env.DataDir, Listen: env.Listen, ManagerState: "unknown", LogPath: filepath.Join(env.DataDir, "service.log")}
	if env.OS == "linux" {
		status.LogPath = ""
		status.LogHint = "journalctl --user -u cove-gatt.service"
	}
	if env.OS == "windows" {
		status.LogPath = ""
		status.LogHint = "当前用户任务计划程序历史；进程 stdout/stderr 未保存为日志文件"
	}
	registered, pid, state, err := env.managerStatus(ctx)
	if err != nil && !errors.Is(err, errServiceAbsent) {
		return status, err
	}
	status.Registered, status.ManagerPID, status.ManagerState = registered, pid, state
	record, err := readRuntime(env.DataDir)
	if err != nil {
		if os.IsNotExist(err) {
			status.Notice = "没有运行实例记录；不会把注册文件当运行证明"
			return status, nil
		}
		return status, err
	}
	status.Instance = &record.runtimeInstance
	request, err := http.NewRequestWithContext(ctx, "GET", "http://"+env.Listen+"/healthz", nil)
	if err != nil {
		return status, err
	}
	response, err := env.Client.Do(request)
	if err != nil {
		status.Notice = "本机监听地址不可达；实例记录可能来自旧进程"
		return status, nil
	}
	defer response.Body.Close()
	var live struct {
		PID     int    `json:"pid"`
		BuildID string `json:"build_id"`
		Version string `json:"version"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&live) != nil {
		status.Notice = "端口有响应，但无法证明是记录中的 Cove"
		return status, nil
	}
	actualExecutable, owners, identityErr := env.liveIdentity(ctx, record.PID)
	if identityErr != nil {
		status.Notice = identityErr.Error()
		return status, nil
	}
	ownPort := false
	for _, owner := range owners {
		if owner == record.PID {
			ownPort = true
		}
		if len(owners) == 1 {
			status.PortOwnerPID = &owner
		}
	}
	diskSHA, hashErr := fileSHA(env.Executable)
	if hashErr != nil {
		return status, hashErr
	}
	status.Running = ownPort && live.PID == record.PID && live.BuildID == record.BuildID
	status.Verified = status.Running && actualExecutable == env.Executable && record.BinarySHA == diskSHA && record.Executable == env.Executable && record.ConfigPath == env.ConfigPath && record.DataDir == env.DataDir && record.Listen == env.Listen && (pid == 0 || pid == record.PID)
	if !status.Verified {
		status.Notice = "运行 PID/build/路径/配置与目标不一致，拒绝将其当作目标实例"
		return status, nil
	}
	ready, err := http.NewRequestWithContext(ctx, "GET", "http://"+env.Listen+"/readyz", nil)
	if err != nil {
		return status, err
	}
	readyResponse, err := env.Client.Do(ready)
	if err == nil {
		status.Ready = readyResponse.StatusCode == 200
		readyResponse.Body.Close()
	}
	status.Notice = "已核对实际端口归属、进程可执行文件、健康 build 和私有启动记录"
	return status, nil
}
func (env platformEnvironment) stopInstance(ctx context.Context) error {
	status, err := env.status(ctx)
	if err != nil {
		return err
	}
	if !status.Running {
		if status.ManagerPID > 0 || status.ManagerState == "running" || status.ManagerState == "active" {
			return fmt.Errorf("manager 报告运行，但实例身份/监听证据未闭合；拒绝停止")
		}
		probe, probeErr := lockDataDir(env.DataDir)
		if probeErr != nil {
			return fmt.Errorf("数据目录仍被进程持有；不能证明已停止: %w", probeErr)
		}
		probe.Close()
		return nil
	}
	if !status.Verified {
		return fmt.Errorf("运行实例身份不符，拒绝停止")
	}
	record, err := readRuntime(env.DataDir)
	if err != nil {
		return err
	}
	if record.ControlToken == "" {
		return fmt.Errorf("实例没有受控关闭入口；请在原启动窗口正常退出，不会强杀")
	}
	request, err := http.NewRequestWithContext(ctx, "POST", "http://"+env.Listen+"/__cove/stop", nil)
	if err != nil {
		return err
	}
	request.Header.Set("X-Cove-Control", record.ControlToken)
	response, err := env.Client.Do(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != 202 {
		return fmt.Errorf("服务未接受受控关闭请求")
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("服务关停等待失败，未强杀: %w", ctx.Err())
		case <-ticker.C:
			lock, err := lockDataDir(env.DataDir)
			if err == nil {
				lock.Close()
				return nil
			}
		}
	}
}
func runPlatform(command string, args []string, configPath string, c app.Config) (bool, error) {
	if command != "service" && command != "status" && command != "doctor" && command != "update" && command != "restore" {
		return false, nil
	}
	env, err := platformEnv(configPath, c)
	if err != nil {
		return true, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	switch command {
	case "status", "doctor":
		if len(args) != 0 {
			return true, fmt.Errorf("status/doctor 不接受额外参数")
		}
		status, e := env.status(ctx)
		if e != nil {
			return true, e
		}
		_, e = env.Output.Write(mustPlatformJSON(status))
		return true, e
	case "service":
		if len(args) != 1 {
			return true, fmt.Errorf("用法：service install|status|start|stop|uninstall")
		}
		return true, env.service(ctx, args[0])
	case "restore":
		return true, env.restoreCommand(ctx, args)
	case "update":
		return true, env.updateCommand(ctx, args)
	}
	return true, nil
}

// A Windows Task has one explicit current-user action. It never invokes a shell.
type windowsTask struct {
	XMLName    xml.Name `xml:"Task"`
	Principals struct {
		Principal struct {
			UserID    string `xml:"UserId"`
			LogonType string `xml:"LogonType"`
			RunLevel  string `xml:"RunLevel"`
		} `xml:"Principal"`
	} `xml:"Principals"`
	Actions struct {
		Exec []struct {
			Command          string `xml:"Command"`
			Arguments        string `xml:"Arguments"`
			WorkingDirectory string `xml:"WorkingDirectory"`
		} `xml:"Exec"`
	} `xml:"Actions"`
}

func windowsArg(v string) string {
	if v != "" && !strings.ContainsAny(v, " \t\n\v\"") {
		return v
	}
	var out strings.Builder
	out.WriteByte('"')
	backslashes := 0
	for _, ch := range v {
		if ch == '\\' {
			backslashes++
			continue
		}
		if ch == '"' {
			out.WriteString(strings.Repeat("\\", backslashes*2+1))
			out.WriteRune(ch)
		} else {
			out.WriteString(strings.Repeat("\\", backslashes))
			out.WriteRune(ch)
		}
		backslashes = 0
	}
	out.WriteString(strings.Repeat("\\", backslashes*2))
	out.WriteByte('"')
	return out.String()
}
func xmlText(v string) string {
	var out bytes.Buffer
	_ = xml.EscapeText(&out, []byte(v))
	return out.String()
}
func unitArg(v string) string {
	v = strings.ReplaceAll(v, "%", "%%")
	v = strings.ReplaceAll(v, "$", "$$")
	return strconv.Quote(v)
}

func appBuildID() string { return app.BuildID }
