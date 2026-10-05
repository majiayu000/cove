package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gatt/internal/app"
	"gatt/web"
	"golang.org/x/term"
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
	if command != "serve" && command != "recover-admin" && command != "enterprise-init" {
		return fmt.Errorf("支持的命令：serve、open、recover-admin、enterprise-init <用户名>")
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
	if command == "enterprise-init" {
		if c.Enterprise == nil || len(platformArgs) != 1 {
			return errors.New("使用独立企业配置：gatt -config <配置文件> enterprise-init <用户名>")
		}
		var password []byte
		if term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Fprint(os.Stderr, "企业管理员密码（至少12字节，不显示）：")
			password, err = term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
		} else {
			password, err = io.ReadAll(io.LimitReader(os.Stdin, 1025))
		}
		if err != nil {
			return errors.New("密码读取失败")
		}
		if err = app.InitializeEnterprise(store, platformArgs[0], strings.TrimRight(string(password), "\r\n")); err != nil {
			return err
		}
		fmt.Println("企业管理员已初始化。启动 serve 后通过企业公开地址登录。")
		return nil
	}
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
	var enterprise *app.Enterprise
	if c.Enterprise != nil {
		enterprise, err = app.NewEnterprise(gateway, assets, ctx)
		if err != nil {
			gateway.CloseAdmission()
			gateway.CancelAll()
			return err
		}
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if closeErr := enterprise.Close(closeCtx); closeErr != nil {
				fmt.Fprintln(os.Stderr, "企业租户关停未完成：", closeErr)
			}
		}()
	}
	runtimeRecord, e := publishRuntime(*config, c, *background)
	if e != nil {
		return e
	}
	defer os.Remove(filepath.Join(c.DataDir, "runtime.json"))
	if enterprise != nil {
		server.Handler = enterprise
	} else {
		server.Handler = platformRestoreControlledHandler(gateway, runtimeRecord, platformRestoreControls{Stop: stop, Drain: gateway.DrainForSwitch, SetStaged: gateway.SetStagedAdmission, IsStaged: gateway.StagedAdmission, Ready: gateway.ReadyForSwitch})
	}
	gateway.StartMaintenance(ctx)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	fmt.Printf("Cove %s · build %s · http://%s\n", app.Version, app.BuildID, c.Listen)
	if !*background && enterprise == nil {
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
	if enterprise != nil {
		err = errors.Join(err, enterprise.Close(drain))
	}
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
