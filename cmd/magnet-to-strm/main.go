package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"magnet-to-strm/internal/bootstrap"
	"magnet-to-strm/internal/config"
	"magnet-to-strm/internal/ingest"
	"magnet-to-strm/internal/logging"
	"magnet-to-strm/internal/strm"
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

	job, err := ingest.NewJob(strings.TrimSpace(flags.Arg(0)))
	if err != nil {
		return err
	}
	existing, err := core.DB.Job(ctx, job.GID)
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
	cfg, secrets, err := loadRuntime(*configPath)
	if err != nil {
		return err
	}
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

	if app.P115Enabled {
		go cleanupLoop(
			ctx, app.Materializer, cfg.Library.CacheRetention, cfg.Library.SweepInterval,
			func(err error) { fmt.Fprintf(stderr, "清理过期缓存失败: %v\n", err) },
		)
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = app.HTTP.Shutdown(shutdownCtx)
	}()
	fmt.Fprintf(stderr, "HTTP 服务已启动: http://%s\n", bootstrap.DisplayAddr(cfg.HTTP.Addr))
	fmt.Fprintf(stderr, "STRM 目录: %s\n", app.Core.STRM.RootDir)
	fmt.Fprintf(stderr, "aria2 JSON-RPC: http://%s/jsonrpc\n", bootstrap.DisplayAddr(cfg.HTTP.Addr))
	err = app.HTTP.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func loadRuntime(configPath string) (config.Config, config.Secrets, error) {
	if err := config.LoadDotEnv(".env"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return config.Config{}, config.Secrets{}, fmt.Errorf("读取 .env: %w", err)
	}
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		return config.Config{}, config.Secrets{}, err
	}
	return cfg, config.LoadSecrets(), nil
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(
		writer,
		"用法: magnet-to-strm <add|serve|rewrite-strm> [选项]",
	)
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "  magnet-to-strm add [-config config.toml] [-json] '<magnet-link>'")
	fmt.Fprintln(writer, "  magnet-to-strm serve [-config config.toml]")
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
