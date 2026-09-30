package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"gatt/internal/app"
	"gatt/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Cove:", err)
		os.Exit(1)
	}
}
func run() error {
	flags := flag.NewFlagSet("gatt", flag.ContinueOnError)
	config := flags.String("config", "config.example.json", "运行配置文件")
	dataDir := flags.String("data-dir", "", "显式私有数据目录")
	admissionClosed := flags.Bool("admission-closed", false, "受控恢复启动，先关闭写入准入")
	restorePointer := flags.String("restore-pointer", "", "受控启动指针文件")
	background := flags.Bool("background", false, "后台运行，不自动打开浏览器")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return err
	}
	command := "serve"
	if flags.NArg() > 0 {
		command = flags.Arg(0)
	}
	resolvedConfig, resolvedDir, err := resolveRestoreStartup(*restorePointer, *config, *dataDir)
	if err != nil {
		return err
	}
	*config = resolvedConfig
	*dataDir = resolvedDir
	c, err := app.LoadConfigWithDataDir(*config, *dataDir)
	if err != nil {
		return err
	}
	dir, err := filepath.Abs(c.DataDir)
	if err != nil {
		return err
	}
	c.DataDir = dir
	platformArgs := []string{}
	if flags.NArg() > 1 {
		platformArgs = flags.Args()[1:]
	}
	if handled, e := runPlatform(command, platformArgs, *config, c); handled {
		return e
	}
	if command == "open" {
		return openManagement(c)
	}
	if command != "serve" && command != "recover-admin" {
		return fmt.Errorf("支持的命令：serve、open、recover-admin")
	}
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return fmt.Errorf("固定端口无法监听，请检查配置或占用：%w", err)
	}
	defer listener.Close()
	lock, err := lockDataDir(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	vault, err := app.NewFileSecrets(dir)
	if err != nil {
		return err
	}
	store, err := app.OpenStore(c.DataDir)
	if err != nil {
		return err
	}
	storageDrained := true
	defer func() {
		if storageDrained {
			_ = store.DB.Close()
		}
	}()
	if command == "recover-admin" {
		if err := app.RecoverAdmin(store, vault); err != nil {
			return fmt.Errorf("本机管理凭据恢复失败，请检查本地存储后重新运行 recover-admin: %w", err)
		}
		fmt.Println("本机管理凭据已轮换。请启动服务；来源和 API Key 已保留。")
		return nil
	}
	assets, err := fs.Sub(web.Files, "dist")
	if err != nil {
		return err
	}
	gateway, err := app.New(c, store, vault, assets)
	if err != nil {
		return err
	}
	gateway.SetStagedAdmission(*admissionClosed)
	server := &http.Server{Handler: gateway, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, MaxHeaderBytes: 1 << 20, IdleTimeout: 60 * time.Second}
	storageDrained = false
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtimeRecord, e := publishRuntime(*config, c, *background)
	if e != nil {
		return e
	}
	defer os.Remove(filepath.Join(c.DataDir, "runtime.json"))
	server.Handler = platformRestoreControlledHandler(gateway, runtimeRecord, platformRestoreControls{Stop: stop, Drain: gateway.DrainForSwitch, SetStaged: gateway.SetStagedAdmission, IsStaged: gateway.StagedAdmission, Ready: gateway.ReadyForSwitch})
	gateway.StartMaintenance(ctx)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Printf("Cove %s · build %s · http://%s\n", app.Version, app.BuildID, c.Listen)
	if !*background {
		if e := openManagement(c); e != nil {
			fmt.Fprintln(os.Stderr, "管理界面未自动打开；可运行 gatt -config <配置文件> open 重试")
		}
	}
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	case <-ctx.Done():
	}
	gateway.CloseAdmission()
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if shutdownErr := server.Shutdown(shutdown); shutdownErr != nil {
		gateway.CancelAll()
		_ = server.Close()
	}
	drain, drainCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer drainCancel()
	if drainErr := gateway.WaitOwnedTasks(drain); drainErr != nil {
		// main exits on error. Leave DB open for process teardown rather than close
		// it underneath tasks which failed to honor cancellation.
		return errors.Join(err, fmt.Errorf("关停等待超时，后台任务尚未结束；数据库留待进程退出关闭: %w", drainErr))
	}
	storageDrained = true
	gateway.CloseNetwork()
	return err
}

func openManagement(c app.Config) error {
	base := "http://" + c.Listen
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(base + "/readyz")
	if err != nil {
		return fmt.Errorf("本机服务尚未启动")
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("Cove 服务尚未就绪")
	}
	return openBrowser(base)
}
