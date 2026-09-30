package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

func (e platformEnvironment) serviceArgs() []string {
	return []string{"-config", e.ConfigPath, "-data-dir", e.DataDir, "-background", "serve"}
}
func (e platformEnvironment) serviceIdentity() string {
	h := sha256.Sum256(mustPlatformJSON([]string{e.Executable, e.ConfigPath, e.DataDir, e.Listen, e.UserID}))
	return hex.EncodeToString(h[:])
}
func (e platformEnvironment) servicePath() string {
	switch e.OS {
	case "darwin":
		return filepath.Join(e.Home, "Library", "LaunchAgents", serviceLabel+".plist")
	case "linux":
		return filepath.Join(e.Home, ".config", "systemd", "user", "cove-gatt.service")
	case "windows":
		return filepath.Join(e.DataDir, "service-task.xml")
	}
	return ""
}
func (e platformEnvironment) taskName() string { return "Cove-gatt-" + e.UserID }
func (e platformEnvironment) serviceDocument() ([]byte, error) {
	args := e.serviceArgs()
	switch e.OS {
	case "darwin":
		var list strings.Builder
		for _, arg := range append([]string{e.Executable}, args...) {
			list.WriteString("<string>" + xmlText(arg) + "</string>")
		}
		return []byte(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + serviceLabel + `</string><key>ProgramArguments</key><array>` + list.String() + `</array><key>WorkingDirectory</key><string>` + xmlText(filepath.Dir(e.Executable)) + `</string><key>EnvironmentVariables</key><dict><key>COVE_SERVICE_ID</key><string>` + e.serviceIdentity() + `</string></dict><key>RunAtLoad</key><true/><key>StandardOutPath</key><string>` + xmlText(filepath.Join(e.DataDir, "service.log")) + `</string><key>StandardErrorPath</key><string>` + xmlText(filepath.Join(e.DataDir, "service.log")) + `</string></dict></plist>` + "\n"), nil
	case "linux":
		quoted := []string{unitArg(e.Executable)}
		for _, arg := range args {
			quoted = append(quoted, unitArg(arg))
		}
		return []byte("[Unit]\nDescription=Cove user gateway\n[Service]\nType=exec\nExecStart=" + strings.Join(quoted, " ") + "\nEnvironment=COVE_SERVICE_ID=" + e.serviceIdentity() + "\nKillSignal=SIGTERM\nTimeoutStopSec=30\n[Install]\nWantedBy=default.target\n"), nil
	case "windows":
		quoted := make([]string, len(args))
		for i, arg := range args {
			quoted[i] = windowsArg(arg)
		}
		return []byte(`<?xml version="1.0" encoding="UTF-8"?><Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"><Triggers><LogonTrigger><Enabled>true</Enabled><UserId>` + xmlText(e.UserID) + `</UserId></LogonTrigger></Triggers><Principals><Principal id="CoveUser"><UserId>` + xmlText(e.UserID) + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals><Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><ExecutionTimeLimit>PT0S</ExecutionTimeLimit></Settings><Actions Context="CoveUser"><Exec><Command>` + xmlText(e.Executable) + `</Command><Arguments>` + xmlText(strings.Join(quoted, " ")) + `</Arguments><WorkingDirectory>` + xmlText(filepath.Dir(e.Executable)) + `</WorkingDirectory></Exec></Actions></Task>` + "\n"), nil
	}
	return nil, fmt.Errorf("用户服务不支持平台 %s", e.OS)
}
func (e platformEnvironment) checkRegistrationFile() (bool, error) {
	path := e.servicePath()
	if path == "" {
		return false, fmt.Errorf("平台不受支持")
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("服务注册冲突：目标不是普通文件")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	expected, err := e.serviceDocument()
	if err != nil {
		return false, err
	}
	if string(data) != string(expected) {
		return false, fmt.Errorf("服务注册冲突：已有可执行路径、参数或用户身份不同；不会覆盖")
	}
	return true, nil
}

var servicePIDPattern = regexp.MustCompile(`(?m)^\s*pid = ([0-9]+)\s*$`)

func (e platformEnvironment) managerStatus(ctx context.Context) (bool, int, string, error) {
	owned, err := e.checkRegistrationFile()
	if err != nil {
		return false, 0, "conflict", err
	}
	switch e.OS {
	case "darwin":
		output, err := e.Run(ctx, "launchctl", "print", "gui/"+e.UserID+"/"+serviceLabel)
		if errors.Is(err, errServiceAbsent) {
			if owned {
				return true, 0, "installed", nil
			}
			return false, 0, "absent", errServiceAbsent
		}
		if err != nil {
			return owned, 0, "unknown", err
		}
		text := string(output)
		if !owned || !strings.Contains(text, e.serviceIdentity()) || !strings.Contains(text, e.servicePath()) {
			return true, 0, "conflict", fmt.Errorf("launchd 已加载注册与目标身份不同，拒绝覆盖/停止")
		}
		pid := 0
		if match := servicePIDPattern.FindStringSubmatch(text); match != nil {
			pid, _ = strconv.Atoi(match[1])
		}
		state := "loaded"
		if pid > 0 {
			state = "running"
		}
		return true, pid, state, nil
	case "linux":
		output, err := e.Run(ctx, "systemctl", "--user", "show", "cove-gatt.service", "--property=LoadState,ActiveState,MainPID,FragmentPath,DropInPaths,Environment")
		if err != nil {
			return owned, 0, "unknown", err
		}
		props := map[string]string{}
		for _, line := range strings.Split(string(output), "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok {
				props[key] = value
			}
		}
		if props["LoadState"] == "not-found" {
			if owned {
				return true, 0, "installed", nil
			}
			return false, 0, "absent", errServiceAbsent
		}
		if !owned || props["FragmentPath"] != e.servicePath() || props["DropInPaths"] != "" || !strings.Contains(props["Environment"], "COVE_SERVICE_ID="+e.serviceIdentity()) {
			return true, 0, "conflict", fmt.Errorf("systemd 已加载注册/附加配置与目标不同，拒绝覆盖/停止")
		}
		pid, err := strconv.Atoi(props["MainPID"])
		if err != nil {
			return true, 0, "unknown", fmt.Errorf("systemd 未返回有效 MainPID")
		}
		return true, pid, props["ActiveState"], nil
	case "windows":
		script := `$ErrorActionPreference='Stop'; $tasks=@(Get-ScheduledTask -ErrorAction Stop | Where-Object {$_.TaskName -eq $inputData.task_name -and $_.TaskPath -eq '\'}); if($tasks.Count -eq 0){@{exists=$false} | ConvertTo-Json -Compress} else {if($tasks.Count -ne 1){throw 'Ambiguous task'}; $document=[xml](Export-ScheduledTask -TaskName $inputData.task_name -TaskPath '\' -ErrorAction Stop); @{exists=$true; document=$document.DocumentElement.OuterXml; state=[int]$tasks[0].State} | ConvertTo-Json -Compress}`
		output, err := e.Run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", powershellEncoded(script, map[string]any{"task_name": e.taskName()}))
		if err != nil {
			return owned, 0, "unknown", err
		}
		var query struct {
			Exists   *bool  `json:"exists"`
			Document string `json:"document"`
			State    int    `json:"state"`
		}
		if json.Unmarshal(output, &query) != nil || query.Exists == nil {
			return owned, 0, "unknown", fmt.Errorf("计划任务未返回有效查询结果")
		}
		if !*query.Exists {
			if owned {
				return true, 0, "definition_only", nil
			}
			return false, 0, "absent", errServiceAbsent
		}
		var actual windowsTask
		if err = xml.Unmarshal([]byte(query.Document), &actual); err != nil {
			return true, 0, "unknown", fmt.Errorf("计划任务返回无效 XML: %w", err)
		}
		expectedBytes, _ := e.serviceDocument()
		var expected windowsTask
		_ = xml.Unmarshal(expectedBytes, &expected)
		actualJSON, _ := json.Marshal(actual)
		expectedJSON, _ := json.Marshal(expected)
		if !owned || string(actualJSON) != string(expectedJSON) {
			return true, 0, "conflict", fmt.Errorf("计划任务的用户、权限或可执行参数不一致，拒绝覆盖/停止")
		}
		// Numeric task state avoids localized schtasks status text. Running PID is
		// obtained independently from the listening socket and process executable.
		state := strconv.Itoa(query.State)
		switch state {
		case "3":
			state = "ready"
		case "4":
			state = "running"
		case "2":
			state = "queued"
		case "1":
			state = "disabled"
		case "0":
			state = "unknown"
		default:
			return true, 0, "unknown", fmt.Errorf("计划任务未返回有效运行状态")
		}
		return true, 0, state, nil
	}
	return false, 0, "unsupported", fmt.Errorf("平台不支持用户后台服务")
}
func (e platformEnvironment) service(ctx context.Context, action string) error {
	registered, pid, state, err := e.managerStatus(ctx)
	if err != nil && !errors.Is(err, errServiceAbsent) {
		return err
	}
	switch action {
	case "status":
		status, err := e.status(ctx)
		if err != nil {
			return err
		}
		_, err = e.Output.Write(mustPlatformJSON(status))
		return err
	case "install":
		if registered && state != "definition_only" && e.OS != "linux" {
			_, err = e.Output.Write([]byte("已有相同用户注册；未覆盖或启动。\n"))
			return err
		}
		// launchd opens log files before starting us. Prepare their private
		// parent now so it cannot create the data directory with public modes.
		dataLock, err := lockDataDir(e.DataDir)
		if err != nil {
			return err
		}
		if err = dataLock.Close(); err != nil {
			return err
		}
		document, err := e.serviceDocument()
		if err != nil {
			return err
		}
		if err = writePrivate(e.servicePath(), document); err != nil {
			return err
		}
		switch e.OS {
		case "linux":
			if _, err = e.Run(ctx, "systemctl", "--user", "daemon-reload"); err == nil {
				_, err = e.Run(ctx, "systemctl", "--user", "enable", "cove-gatt.service")
			}
		case "windows":
			_, err = e.Run(ctx, "schtasks.exe", "/Create", "/TN", e.taskName(), "/XML", e.servicePath())
		}
		if err != nil {
			return err
		}
		_, err = e.Output.Write([]byte("用户启动注册已安装；运行 service start 启动。\n"))
		return err
	case "start":
		if !registered || state == "definition_only" {
			return fmt.Errorf("用户服务尚未安装；先运行 service install")
		}
		if pid > 0 || state == "running" {
			return nil
		}
		switch e.OS {
		case "darwin":
			if state == "installed" {
				_, err = e.Run(ctx, "launchctl", "bootstrap", "gui/"+e.UserID, e.servicePath())
			} else {
				_, err = e.Run(ctx, "launchctl", "kickstart", "gui/"+e.UserID+"/"+serviceLabel)
			}
		case "linux":
			_, err = e.Run(ctx, "systemctl", "--user", "start", "cove-gatt.service")
		case "windows":
			_, err = e.Run(ctx, "schtasks.exe", "/Run", "/TN", e.taskName())
		}
		return err
	case "stop":
		return e.stopInstance(ctx)
	case "uninstall":
		if !registered {
			return nil
		}
		if err = e.stopInstance(ctx); err != nil {
			return err
		}
		switch e.OS {
		case "darwin":
			if state != "installed" {
				_, err = e.Run(ctx, "launchctl", "bootout", "gui/"+e.UserID+"/"+serviceLabel)
			}
		case "linux":
			_, err = e.Run(ctx, "systemctl", "--user", "disable", "cove-gatt.service")
		case "windows":
			if state != "definition_only" {
				_, err = e.Run(ctx, "schtasks.exe", "/Delete", "/TN", e.taskName(), "/F")
			}
		}
		if err != nil {
			return err
		}
		if err = os.Remove(e.servicePath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		if e.OS == "linux" {
			_, err = e.Run(ctx, "systemctl", "--user", "daemon-reload")
		}
		return err
	default:
		return fmt.Errorf("不支持的 service 动作")
	}
}
func (e platformEnvironment) liveIdentity(ctx context.Context, pid int) (string, []int, error) {
	_, port, err := net.SplitHostPort(e.Listen)
	if err != nil {
		return "", nil, err
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", nil, fmt.Errorf("无效监听端口")
	}
	processID := strconv.Itoa(pid)
	switch e.OS {
	case "darwin", "linux":
		output, err := e.Run(ctx, "lsof", "-nP", "-a", "-iTCP:"+port, "-sTCP:LISTEN", "-Fp")
		if err != nil {
			return "", nil, fmt.Errorf("无法核验端口归属（需要 lsof）: %w", err)
		}
		var owners []int
		for _, line := range strings.Split(string(output), "\n") {
			if strings.HasPrefix(line, "p") {
				owner, err := strconv.Atoi(line[1:])
				if err == nil && owner > 0 {
					owners = append(owners, owner)
				}
			}
		}
		// Let lsof match the actual file. Its printable name can escape UTF-8
		// under LC_ALL=C and is not a reliable byte-for-byte path identity.
		output, err = e.Run(ctx, "lsof", "-a", "-p", processID, "-d", "txt", "-Fp", e.Executable)
		if err != nil {
			return "", owners, fmt.Errorf("无法核验实际进程可执行文件: %w", err)
		}
		for _, line := range strings.Split(string(output), "\n") {
			if line == "p"+processID {
				return e.Executable, owners, nil
			}
		}
		return "", owners, fmt.Errorf("实际进程未打开目标可执行文件")
	case "windows":
		script := `$ErrorActionPreference='Stop'; $id=[int]$inputData.pid; $port=[int]$inputData.port; $p=Get-CimInstance Win32_Process -Filter ('ProcessId='+$id); if(!$p){throw 'Process absent'}; @{executable=$p.ExecutablePath; owners=@(Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction Stop | Select-Object -ExpandProperty OwningProcess)} | ConvertTo-Json -Compress`
		output, err := e.Run(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", powershellEncoded(script, map[string]any{"pid": pid, "port": number}))
		if err != nil {
			return "", nil, err
		}
		var result struct {
			Executable string `json:"executable"`
			Owners     []int  `json:"owners"`
		}
		if err = json.Unmarshal(output, &result); err != nil {
			return "", nil, fmt.Errorf("无法解析进程/端口归属")
		}
		return result.Executable, result.Owners, nil
	}
	return "", nil, fmt.Errorf("平台尚不支持实际进程核验")
}

// UTF-16LE EncodedCommand keeps observation parameters as JSON data instead of
// relying on PowerShell -Command reparsing argv or interpolating user strings.
func powershellEncoded(script string, data any) string {
	raw, _ := json.Marshal(data)
	prefix := "$ProgressPreference='SilentlyContinue'; [Console]::OutputEncoding=(New-Object Text.UTF8Encoding($false)); $inputData=ConvertFrom-Json ([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + base64.StdEncoding.EncodeToString(raw) + "'))); "
	units := utf16.Encode([]rune(prefix + script))
	encoded := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(encoded[i*2:], unit)
	}
	return base64.StdEncoding.EncodeToString(encoded)
}
