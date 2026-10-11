package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"magnet-to-strm/internal/bootstrap"
	"magnet-to-strm/internal/config"
	"magnet-to-strm/internal/ingest"
	"magnet-to-strm/internal/logging"
	"magnet-to-strm/internal/rclone"
	"magnet-to-strm/internal/strm"
	"magnet-to-strm/internal/transport/httpserver"
)

func main() {
	stderr := logging.NewTimestampWriter(os.Stderr)
	if err := run(os.Args[1:], os.Stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "错误: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	stderr = logging.NewTimestampWriter(stderr)
	if len(args) == 0 {
		printUsage(stderr)
		return errors.New("必须指定 add、serve 或 rewrite-strm 子命令")
	}
	switch args[0] {
	case "add":
		return runAdd(args[1:], stdout, stderr)
	case "serve":
		return runServe(args[1:], stderr)
	case "rewrite-strm":
		return runRewriteSTRM(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("未知子命令 %q", args[0])
	}
}

func runRewriteSTRM(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("magnet-to-strm rewrite-strm", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config.toml", "TOML 配置文件路径")
	flags.Usage = func() {
		fmt.Fprintln(
			stderr,
			"用法: magnet-to-strm rewrite-strm [选项] '<旧URL前缀>' '<新URL前缀>'",
		)
		fmt.Fprintln(stderr)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 2 {
		flags.Usage()
		return errors.New("必须提供旧 URL 前缀和新 URL 前缀")
	}
	cfg, err := config.LoadFile(*configPath)
	if err != nil {
		return err
	}
	store, err := strm.New(cfg.Library.STRMDir, cfg.HTTP.PublicBaseURL)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(
		context.Background(), os.Interrupt, syscall.SIGTERM,
	)
	defer stop()
	result, err := store.ReplacePrefix(ctx, flags.Arg(0), flags.Arg(1))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "已扫描 %d 个 STRM 文件，更新 %d 个\n",
		result.Scanned, result.Rewritten)
	return nil
}

func runAdd(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("magnet-to-strm add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config.toml", "TOML 配置文件路径")
	asJSON := flags.Bool("json", false, "以 JSON 格式输出")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "用法: magnet-to-strm add [选项] '<magnet-link>'")
		fmt.Fprintln(stderr)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 1 {
		flags.Usage()
		return errors.New("必须提供一个磁力链接")
	}
	cfg, secrets, err := loadRuntime(*configPath)
	if err != nil {
		return err
	}
	if err := cfg.ValidateAdd(); err != nil {
		return err
	}

	signalCtx, stop := signal.NotifyContext(
		context.Background(), os.Interrupt, syscall.SIGTERM,
	)
	defer stop()
	ctx, cancel := context.WithTimeout(signalCtx, cfg.Ingest.JobTimeout)
	defer cancel()
	core, err := bootstrap.OpenCore(ctx, cfg, secrets, stderr)
	if err != nil {
		return err
	}
	defer core.Close()

	job, err := ingest.NewJobForSource(
		strings.TrimSpace(flags.Arg(0)), ingest.TaskSourceCLI,
	)
	if err != nil {
		return err
	}
	existing, err := core.DB.LatestJob(ctx, job.Source, job.InfoHash)
	switch {
	case errors.Is(err, ingest.ErrJobNotFound):
		if err := core.DB.CreateJob(ctx, job); err != nil {
			return err
		}
	case err != nil:
		return err
	case existing.InfoHash != job.InfoHash:
		return errors.New("GID 冲突")
	case existing.State == ingest.JobSucceeded:
		result, err := core.Ingest.Result(ctx, job.InfoHash)
		if err != nil {
			return err
		}
		return writeResult(stdout, result, *asJSON)
	case existing.State == ingest.JobRunning:
		return errors.New("相同磁链的任务正在运行")
	default:
		job = existing
		job.State = ingest.JobQueued
		job.Error = ""
		job.StartedAt = nil
		job.FinishedAt = nil
		if err := core.DB.UpdateJob(ctx, job); err != nil {
			return err
		}
	}
	result, err := ingest.RunJob(ctx, core.DB, core.Ingest, job)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			if cleanupErr := core.Ingest.CleanupTimedOutTask(job.InfoHash); cleanupErr != nil {
				return fmt.Errorf(
					"等待 115 离线任务超时（%s）；%v",
					cfg.Ingest.JobTimeout,
					cleanupErr,
				)
			}
			return fmt.Errorf("等待 115 离线任务超时（%s）", cfg.Ingest.JobTimeout)
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return errors.New("操作已取消")
		}
		return err
	}
	return writeResult(stdout, result, *asJSON)
}

func runServe(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("magnet-to-strm serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config.toml", "TOML 配置文件路径")
	port := flags.Int("port", 0, "监听端口（1–65535，覆盖配置文件）")
	listen := flags.String("listen", "", "监听地址，例如 127.0.0.1:8081（与 -port 互斥）")
	publicURL := flags.String("public-base-url", "", "STRM 对外地址（覆盖配置文件）")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "用法: magnet-to-strm serve [选项]")
		fmt.Fprintln(stderr)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("serve 不接受位置参数")
	}
	var invalidPort bool
	flags.Visit(func(value *flag.Flag) {
		if value.Name == "port" && *port == 0 {
			invalidPort = true
		}
	})
	if invalidPort {
		return errors.New("-port 必须在 1–65535 之间")
	}
	initial, err := serveHTTPOptions(*port, *listen, *publicURL)
	if err != nil {
		return err
	}
	if err := config.EnsureFile(*configPath, initial); err != nil {
		return err
	}
	cfg, secrets, err := loadRuntime(*configPath)
	if err != nil {
		return err
	}
	if initial.ListenAddr != "" {
		cfg.HTTP.Addr = initial.ListenAddr
	}
	if *publicURL != "" {
		// A redirect URL inherited from the public URL follows an explicit override.
		inherited := cfg.HTTP.RedirectBaseURL == cfg.HTTP.PublicBaseURL
		cfg.HTTP.PublicBaseURL = strings.TrimRight(initial.PublicBaseURL, "/")
		if inherited {
			cfg.HTTP.RedirectBaseURL = cfg.HTTP.PublicBaseURL
		}
	}
	if cfg.SetupRequired {
		restart, err := runSetupServer(cfg, stderr)
		if err != nil || !restart {
			return err
		}
		return runServe(args, stderr)
	}
	return runConfiguredServer(cfg, secrets, stderr)
}

func runConfiguredServer(cfg config.Config, secrets config.Secrets, stderr io.Writer) error {
	if err := cfg.ValidateServe(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app, err := bootstrap.OpenServer(ctx, cfg, secrets, stderr)
	if err != nil {
		return err
	}
	defer app.Close()

	if cfg.P115.WorkDirID != "" {
		go cleanupLoop(
			ctx, app.Materializer, cfg.Library.CacheRetention, cfg.Library.SweepInterval,
			func(err error) { app.Core.Logger.Printf("清理过期缓存失败: %v", err) },
		)
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = app.HTTP.Shutdown(shutdownCtx)
	}()
	listener, err := net.Listen("tcp", cfg.HTTP.Addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	app.Core.Logger.Printf("HTTP 服务已启动: http://%s", bootstrap.DisplayAddr(cfg.HTTP.Addr))
	app.Core.Logger.Printf("STRM 目录: %s", app.Core.STRM.RootDir)
	app.Core.Logger.Printf("aria2 JSON-RPC: http://%s/jsonrpc", bootstrap.DisplayAddr(cfg.HTTP.Addr))
	gateway := &rclone.Gateway{Next: app.HTTP.Handler}
	app.HTTP.Handler = gateway
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- app.HTTP.Serve(listener) }()
	defer app.HTTP.Close()
	var childDone <-chan struct{}
	var child *rclone.Process
	if cfg.Rclone.Enabled {
		binary, lookupErr := exec.LookPath("rclone")
		if lookupErr != nil {
			app.Core.Logger.Printf("未找到 rclone，可安装 rclone 或使用内置 rclone 的镜像；本次使用 WebDAV 代理")
		} else {
			host, port, _ := net.SplitHostPort(listener.Addr().String())
			if host == "" || host == "::" || host == "0.0.0.0" {
				host = "127.0.0.1"
			}
			upstream := "http://" + net.JoinHostPort(host, port)
			child, err = rclone.Start(ctx, binary, upstream, cfg.Rclone.WithDefaults().CacheDir, app.Core.Logger.Writer(), cfg.Rclone)
			if err != nil {
				return err
			}
			defer child.Close()
			childDone = child.Done()
			gateway.SetTarget(child.URL)
			app.Core.Logger.Printf("内置 rclone rcd 已启用，播放缓存目录: %s", cfg.Rclone.WithDefaults().CacheDir)
		}
	}
	select {
	case err = <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-childDone:
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("内置 rclone rcd 意外退出: %v", child.Err())
	}
}

func runSetupServer(cfg config.Config, stderr io.Writer, options ...httpserver.ConfigHooks) (bool, error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	listener, err := net.Listen("tcp", cfg.HTTP.Addr)
	if err != nil {
		return false, err
	}
	defer listener.Close()
	saved := make(chan struct{}, 1)
	server := &http.Server{
		Handler: httpserver.SetupHandler(cfg.FilePath, func() {
			select {
			case saved <- struct{}{}:
			default:
			}
		}, options...),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
	}
	defer server.Close()
	fmt.Fprintf(stderr, "初始化配置未完成，仅启动配置服务: http://%s\n", bootstrap.DisplayAddr(cfg.HTTP.Addr))
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	restart := false
	select {
	case <-saved:
		restart = true
	case <-ctx.Done():
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return false, err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return false, err
	}
	if restart {
		fmt.Fprintln(stderr, "初始化配置已保存，正在自动重启服务")
	}
	return restart && ctx.Err() == nil, nil
}

func loadRuntime(configPath string) (config.Config, config.Secrets, error) {
	if err := config.LoadDotEnv(".env"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return config.Config{}, config.Secrets{}, fmt.Errorf("读取 .env: %w", err)
	}
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		return config.Config{}, config.Secrets{}, err
	}
	return cfg, cfg.RuntimeSecrets(), nil
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(
		writer,
		"用法: magnet-to-strm <add|serve|rewrite-strm> [选项]",
	)
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "  magnet-to-strm add [-config config.toml] [-json] '<magnet-link>'")
	fmt.Fprintln(writer, "  magnet-to-strm serve [-config config.toml] [-port 8081] [-listen host:port] [-public-base-url URL]")
	fmt.Fprintln(
		writer,
		"  magnet-to-strm rewrite-strm [-config config.toml] '<旧URL前缀>' '<新URL前缀>'",
	)
}

func cleanupLoop(
	ctx context.Context,
	service interface {
		Cleanup(context.Context, time.Duration) error
	},
	retention time.Duration,
	interval time.Duration,
	onError func(error),
) {
	run := func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		if err := service.Cleanup(cleanupCtx, retention); err != nil && onError != nil {
			onError(err)
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func writeResult(stdout io.Writer, result ingest.Result, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	fmt.Fprintf(stdout, "名称: %s\n", result.Name)
	fmt.Fprintf(stdout, "Info Hash: %s\n", result.InfoHash)
	if result.ReusedTask {
		fmt.Fprintln(stdout, "115 任务: 已复用")
	} else {
		fmt.Fprintln(stdout, "115 任务: 新建")
	}
	fmt.Fprintf(stdout, "结果 ID: %s\n", result.ResultID)
	fmt.Fprintf(stdout, "文件数: %d\n", len(result.Files))
	fmt.Fprintf(stdout, "总大小: %s\n\n", humanSize(result.TotalBytes))
	for _, file := range result.Files {
		fmt.Fprintf(
			stdout, "%12s  %s  %s\n",
			humanSize(file.SizeBytes), file.SHA1, file.RelativePath,
		)
	}
	return nil
}

func humanSize(bytes int64) string {
	const unit = int64(1024)
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := unit, 0
	for number := bytes / unit; number >= unit && exp < 5; number /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// serveHTTPOptions validates startup overrides before creating any files.
func serveHTTPOptions(port int, listen, publicURL string) (config.InitialHTTP, error) {
	if port < 0 || port > 65535 {
		return config.InitialHTTP{}, errors.New("-port 必须在 1–65535 之间")
	}
	if port != 0 && listen != "" {
		return config.InitialHTTP{}, errors.New("-port 和 -listen 不能同时指定")
	}
	if port != 0 {
		listen = ":" + strconv.Itoa(port)
	}
	initial := config.InitialHTTP{ListenAddr: listen, PublicBaseURL: publicURL}
	if listen != "" {
		host, value, err := net.SplitHostPort(listen)
		if err != nil {
			return initial, errors.New("-listen 必须是 host:port 格式，例如 :8081")
		}
		number, err := strconv.Atoi(value)
		if err != nil || number < 1 || number > 65535 {
			return initial, errors.New("监听端口必须在 1–65535 之间")
		}
		if initial.PublicBaseURL == "" {
			if host == "" || host == "0.0.0.0" || host == "::" {
				host = "127.0.0.1"
			}
			initial.PublicBaseURL = "http://" + net.JoinHostPort(host, value)
		}
	}
	if publicURL != "" {
		parsed, err := url.Parse(publicURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return initial, errors.New("-public-base-url 必须是有效的 HTTP 或 HTTPS 地址")
		}
	}
	return initial, nil
}
