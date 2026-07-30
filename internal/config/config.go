package config

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Database Database
	HTTP     HTTP
	P115     P115
	Library  Library
	Ingest   Ingest
}

type Database struct {
	Path string
}

type HTTP struct {
	Addr                string
	PublicBaseURL       string
	RedirectBaseURL     string
	RedirectType        string
	MaterializeCacheTTL time.Duration
}

type P115 struct {
	TokenRefreshAhead  time.Duration
	WorkDirID          string
	RequestRate        float64
	RequestBurst       int
	RequestConcurrency int
	OfflinePoll        time.Duration
	OfflinePollMin     time.Duration
	OfflinePollMax     time.Duration
}

type Library struct {
	STRMDir           string
	CacheRetention    time.Duration
	CacheMaxSizeBytes int64
	SweepInterval     time.Duration
}

type Ingest struct {
	JobTimeout time.Duration
}

type Secrets struct {
	P115RefreshToken string
	Aria2RPCSecret   string
	QBitUsername     string
	QBitPassword     string
}

type fileConfig struct {
	Database struct {
		Path string `toml:"path"`
	} `toml:"database"`
	HTTP struct {
		ListenAddr          string `toml:"listen_addr"`
		PublicBaseURL       string `toml:"public_base_url"`
		RedirectBaseURL     string `toml:"redirect_base_url"`
		RedirectType        string `toml:"redirect_type"`
		MaterializeCacheTTL string `toml:"materialize_cache_ttl"`
	} `toml:"http"`
	P115 struct {
		WorkDirID          string  `toml:"work_dir_id"`
		TokenRefreshAhead  string  `toml:"token_refresh_ahead"`
		RequestRate        float64 `toml:"request_rate"`
		RequestBurst       int     `toml:"request_burst"`
		RequestConcurrency int     `toml:"request_concurrency"`
		OfflinePoll        string  `toml:"offline_poll_interval"`
		OfflinePollMin     string  `toml:"offline_poll_min_interval"`
		OfflinePollMax     string  `toml:"offline_poll_max_interval"`
	} `toml:"p115"`
	Library struct {
		STRMDir        string `toml:"strm_dir"`
		CacheRetention string `toml:"cache_retention"`
		CacheMaxSize   string `toml:"cache_max_size"`
		SweepInterval  string `toml:"sweep_interval"`
	} `toml:"library"`
	Ingest struct {
		JobTimeout string `toml:"job_timeout"`
	} `toml:"ingest"`
}

func LoadFile(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("打开配置文件 %s: %w", path, err)
	}
	defer file.Close()

	raw := defaultFileConfig()
	decoder := toml.NewDecoder(file).DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("解析配置文件 %s: %w", path, err)
	}
	refreshAhead, err := parseDuration("p115.token_refresh_ahead", raw.P115.TokenRefreshAhead)
	if err != nil {
		return Config{}, err
	}
	offlinePoll, err := parseDuration("p115.offline_poll_interval", raw.P115.OfflinePoll)
	if err != nil {
		return Config{}, err
	}
	offlinePollMin, err := parseDuration(
		"p115.offline_poll_min_interval", raw.P115.OfflinePollMin,
	)
	if err != nil {
		return Config{}, err
	}
	offlinePollMax, err := parseDuration(
		"p115.offline_poll_max_interval", raw.P115.OfflinePollMax,
	)
	if err != nil {
		return Config{}, err
	}
	retention, err := parseDuration("library.cache_retention", raw.Library.CacheRetention)
	if err != nil {
		return Config{}, err
	}
	cacheMaxSize, err := parseByteSize("library.cache_max_size", raw.Library.CacheMaxSize)
	if err != nil {
		return Config{}, err
	}
	sweepInterval, err := parseDuration("library.sweep_interval", raw.Library.SweepInterval)
	if err != nil {
		return Config{}, err
	}
	jobTimeout, err := parseDuration("ingest.job_timeout", raw.Ingest.JobTimeout)
	if err != nil {
		return Config{}, err
	}
	materializeCacheTTL, err := parseDuration(
		"http.materialize_cache_ttl",
		raw.HTTP.MaterializeCacheTTL,
	)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Database: Database{Path: strings.TrimSpace(raw.Database.Path)},
		HTTP: HTTP{
			Addr:                strings.TrimSpace(raw.HTTP.ListenAddr),
			PublicBaseURL:       strings.TrimRight(strings.TrimSpace(raw.HTTP.PublicBaseURL), "/"),
			RedirectBaseURL:     strings.TrimRight(strings.TrimSpace(raw.HTTP.RedirectBaseURL), "/"),
			RedirectType:        strings.ToLower(strings.TrimSpace(raw.HTTP.RedirectType)),
			MaterializeCacheTTL: materializeCacheTTL,
		},
		P115: P115{
			TokenRefreshAhead:  refreshAhead,
			WorkDirID:          strings.TrimSpace(raw.P115.WorkDirID),
			RequestRate:        raw.P115.RequestRate,
			RequestBurst:       raw.P115.RequestBurst,
			RequestConcurrency: raw.P115.RequestConcurrency,
			OfflinePoll:        offlinePoll,
			OfflinePollMin:     offlinePollMin,
			OfflinePollMax:     offlinePollMax,
		},
		Library: Library{
			STRMDir:           strings.TrimSpace(raw.Library.STRMDir),
			CacheRetention:    retention,
			CacheMaxSizeBytes: cacheMaxSize,
			SweepInterval:     sweepInterval,
		},
		Ingest: Ingest{JobTimeout: jobTimeout},
	}
	if cfg.Library.STRMDir == "" {
		cfg.Library.STRMDir = filepath.Join(filepath.Dir(cfg.Database.Path), "strms")
	}
	if cfg.HTTP.RedirectBaseURL == "" {
		cfg.HTTP.RedirectBaseURL = cfg.HTTP.PublicBaseURL
	}
	if cfg.HTTP.RedirectType == "stable_dav" {
		cfg.HTTP.RedirectType = "proxy"
	}
	return cfg, cfg.validateCommon()
}

func LoadSecrets() Secrets {
	return Secrets{
		P115RefreshToken: strings.TrimSpace(os.Getenv("MTS_P115_REFRESH_TOKEN")),
		Aria2RPCSecret:   strings.TrimSpace(os.Getenv("MTS_ARIA2_RPC_SECRET")),
		QBitUsername:     strings.TrimSpace(os.Getenv("MTS_QBITTORRENT_USERNAME")),
		QBitPassword:     strings.TrimSpace(os.Getenv("MTS_QBITTORRENT_PASSWORD")),
	}
}

func (c Config) ValidateAdd() error {
	if err := c.ValidateRuntime(); err != nil {
		return err
	}
	if c.P115.WorkDirID == "" {
		return errors.New("配置项 p115.work_dir_id 不能为空")
	}
	return nil
}

func (c Config) ValidateServe() error {
	if err := c.ValidateRuntime(); err != nil {
		return err
	}
	if err := validHTTPURL("http.redirect_base_url", c.HTTP.RedirectBaseURL); err != nil {
		return err
	}
	switch c.HTTP.RedirectType {
	case "direct", "proxy", "stable_dav":
	default:
		return errors.New("http.redirect_type 必须是 direct 或 proxy（stable_dav 仍兼容）")
	}
	if c.HTTP.MaterializeCacheTTL <= 0 {
		return errors.New("http.materialize_cache_ttl 必须大于 0")
	}
	if c.Library.CacheRetention <= 0 || c.Library.SweepInterval <= 0 || c.Library.CacheMaxSizeBytes < 0 {
		return errors.New("library.cache_retention 和 library.sweep_interval 必须大于 0，library.cache_max_size 不能小于 0")
	}
	return nil
}

// ValidateRuntime checks the settings needed for local database access and the
// STRM store. A 115 work directory is deliberately not required here.
func (c Config) ValidateRuntime() error {
	if err := c.validateCommon(); err != nil {
		return err
	}
	return validHTTPURL("http.public_base_url", c.HTTP.PublicBaseURL)
}

func (c Config) validateCommon() error {
	if c.Database.Path == "" {
		return errors.New("配置项 database.path 不能为空")
	}
	if c.HTTP.Addr == "" {
		return errors.New("配置项 http.listen_addr 不能为空")
	}
	if c.Library.STRMDir == "" {
		return errors.New("配置项 library.strm_dir 不能为空")
	}
	if c.P115.TokenRefreshAhead < 0 ||
		c.P115.RequestRate < 0 || c.P115.RequestBurst < 0 ||
		c.P115.RequestConcurrency < 0 {
		return errors.New("TOKEN 提前刷新时间、115 请求速率、突发数和并发数不能小于 0")
	}
	if c.P115.OfflinePoll <= 0 || c.P115.OfflinePollMin < 0 ||
		c.P115.OfflinePollMax < 0 ||
		(c.P115.OfflinePollMin > 0 && c.P115.OfflinePollMax > 0 &&
			c.P115.OfflinePollMax < c.P115.OfflinePollMin) ||
		c.Ingest.JobTimeout <= 0 {
		return errors.New(
			"离线轮询间隔和最短轮询时间必须大于 0，最长轮询时间不能小于最短轮询时间，任务超时必须大于 0",
		)
	}
	return nil
}

func defaultFileConfig() fileConfig {
	var raw fileConfig
	raw.Database.Path = "magnet.db"
	raw.HTTP.ListenAddr = ":8080"
	raw.HTTP.RedirectType = "direct"
	raw.HTTP.MaterializeCacheTTL = "10m"
	raw.P115.TokenRefreshAhead = "10m"
	raw.P115.RequestRate = 1
	raw.P115.RequestBurst = 2
	raw.P115.RequestConcurrency = 2
	raw.P115.OfflinePoll = "5s"
	raw.P115.OfflinePollMin = "2s"
	raw.P115.OfflinePollMax = "5m"
	raw.Library.CacheRetention = "720h"
	raw.Library.CacheMaxSize = "0"
	raw.Library.SweepInterval = "1h"
	raw.Ingest.JobTimeout = "6h"
	return raw
}

func parseDuration(key, value string) (time.Duration, error) {
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("配置项 %s 不是有效的时间长度: %w", key, err)
	}
	return parsed, nil
}

func parseByteSize(key, value string) (int64, error) {
	value = strings.TrimSpace(strings.ToUpper(value))
	if value == "" || value == "0" {
		return 0, nil
	}
	units := []struct {
		suffix string
		factor int64
	}{
		{"TB", 1 << 40}, {"T", 1 << 40},
		{"GB", 1 << 30}, {"G", 1 << 30},
		{"MB", 1 << 20}, {"M", 1 << 20},
		{"KB", 1 << 10}, {"K", 1 << 10}, {"B", 1},
	}
	for _, unit := range units {
		if !strings.HasSuffix(value, unit.suffix) {
			continue
		}
		number := strings.TrimSpace(strings.TrimSuffix(value, unit.suffix))
		parsed, err := strconv.ParseFloat(number, 64)
		if err != nil || parsed < 0 || parsed > float64(math.MaxInt64)/float64(unit.factor) {
			break
		}
		return int64(parsed * float64(unit.factor)), nil
	}
	return 0, fmt.Errorf("配置项 %s 不是有效的空间大小（例如 4TB）", key)
}

func validHTTPURL(key, value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("配置项 %s 必须是有效的 HTTP 或 HTTPS 地址", key)
	}
	return nil
}
