package config

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Credentials   Secrets
	SetupRequired bool
	FilePath      string
	Database      Database
	Logging       Logging
	HTTP          HTTP
	Rclone        Rclone
	P115          P115
	Library       Library
	Ingest        Ingest
}

type Rclone struct {
	Enabled              bool     `toml:"enabled" json:"enabled"`
	ServerReadTimeout    string   `toml:"server_read_timeout" json:"server_read_timeout"`
	ServerWriteTimeout   string   `toml:"server_write_timeout" json:"server_write_timeout"`
	CacheDir             string   `toml:"cache_dir" json:"cache_dir"`
	VFSCacheMode         string   `toml:"vfs_cache_mode" json:"vfs_cache_mode"`
	VFSCacheMaxSize      string   `toml:"vfs_cache_max_size" json:"vfs_cache_max_size"`
	VFSCacheMaxAge       string   `toml:"vfs_cache_max_age" json:"vfs_cache_max_age"`
	VFSCachePollInterval string   `toml:"vfs_cache_poll_interval" json:"vfs_cache_poll_interval"`
	VFSReadAhead         string   `toml:"vfs_read_ahead" json:"vfs_read_ahead"`
	BufferSize           string   `toml:"buffer_size" json:"buffer_size"`
	MaxBufferMemory      string   `toml:"max_buffer_memory" json:"max_buffer_memory"`
	Custom               []string `toml:"custom" json:"custom"`
}

func DefaultRclone() Rclone {
	return Rclone{Enabled: true, CacheDir: "/cache", ServerReadTimeout: "24h", ServerWriteTimeout: "24h", VFSCacheMode: "full", VFSCacheMaxSize: "512G", VFSCacheMaxAge: "72h", VFSCachePollInterval: "5m", VFSReadAhead: "4G", BufferSize: "64M", MaxBufferMemory: "6G"}
}

type Logging struct {
	Level string
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
	TokenRefreshAhead        time.Duration
	WorkDirID                string
	RequestRate              float64
	RequestBurst             int
	RequestConcurrency       int
	OfflineQuotaMinRemaining int
	OfflinePoll              time.Duration
	OfflinePollMin           time.Duration
	OfflinePollMax           time.Duration
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
	P115AuthToken    string `toml:"p115_auth_token,omitempty" json:"-"`
	P115RefreshToken string `toml:"p115_refresh_token,omitempty" json:"-"`
	Aria2RPCSecret   string `toml:"aria2_rpc_secret" json:"aria2_rpc_secret"`
	QBitUsername     string `toml:"qbittorrent_username" json:"qbittorrent_username"`
	QBitPassword     string `toml:"qbittorrent_password" json:"qbittorrent_password"`
}

type FileConfig struct {
	Rclone      Rclone  `toml:"rclone" json:"rclone"`
	Credentials Secrets `toml:"credentials" json:"-"`
	Setup       struct {
		Required bool `toml:"required" json:"required"`
	} `toml:"setup" json:"setup"`
	Database struct {
		Path string `toml:"path" json:"path"`
	} `toml:"database" json:"database"`
	Logging struct {
		Level string `toml:"level" json:"level"`
	} `toml:"logging" json:"logging"`
	HTTP struct {
		ListenAddr          string `toml:"listen_addr" json:"listen_addr"`
		PublicBaseURL       string `toml:"public_base_url" json:"public_base_url"`
		RedirectBaseURL     string `toml:"redirect_base_url" json:"redirect_base_url"`
		RedirectType        string `toml:"redirect_type" json:"redirect_type"`
		MaterializeCacheTTL string `toml:"materialize_cache_ttl" json:"materialize_cache_ttl"`
	} `toml:"http" json:"http"`
	P115 struct {
		WorkDirID                string  `toml:"work_dir_id" json:"work_dir_id"`
		TokenRefreshAhead        string  `toml:"token_refresh_ahead" json:"token_refresh_ahead"`
		RequestRate              float64 `toml:"request_rate" json:"request_rate"`
		RequestBurst             int     `toml:"request_burst" json:"request_burst"`
		RequestConcurrency       int     `toml:"request_concurrency" json:"request_concurrency"`
		OfflineQuotaMinRemaining int     `toml:"offline_quota_min_remaining" json:"offline_quota_min_remaining"`
		OfflinePoll              string  `toml:"offline_poll_interval" json:"offline_poll_interval"`
		OfflinePollMin           string  `toml:"offline_poll_min_interval" json:"offline_poll_min_interval"`
		OfflinePollMax           string  `toml:"offline_poll_max_interval" json:"offline_poll_max_interval"`
	} `toml:"p115" json:"p115"`
	Library struct {
		STRMDir        string `toml:"strm_dir" json:"strm_dir"`
		CacheRetention string `toml:"cache_retention" json:"cache_retention"`
		CacheMaxSize   string `toml:"cache_max_size" json:"cache_max_size"`
		SweepInterval  string `toml:"sweep_interval" json:"sweep_interval"`
	} `toml:"library" json:"library"`
	Ingest struct {
		JobTimeout string `toml:"job_timeout" json:"job_timeout"`
	} `toml:"ingest" json:"ingest"`
}

func LoadFile(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("打开配置文件 %s: %w", path, err)
	}
	defer file.Close()

	cfg, err := Parse(file)
	cfg.FilePath = path
	return cfg, err
}

// Parse validates TOML without changing the configuration file.
func Parse(reader io.Reader) (Config, error) {
	raw := defaultFileConfig()
	decoder := toml.NewDecoder(reader).DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("解析配置文件: %w", err)
	}
	if addr := strings.TrimSpace(os.Getenv("MTS_HTTP_LISTEN_ADDR")); addr != "" {
		raw.HTTP.ListenAddr = addr
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
		SetupRequired: raw.Setup.Required,
		Rclone:        raw.Rclone,
		Credentials:   raw.Credentials,
		Database:      Database{Path: strings.TrimSpace(raw.Database.Path)},
		Logging: Logging{
			Level: strings.ToLower(strings.TrimSpace(raw.Logging.Level)),
		},
		HTTP: HTTP{
			Addr:                strings.TrimSpace(raw.HTTP.ListenAddr),
			PublicBaseURL:       strings.TrimRight(strings.TrimSpace(raw.HTTP.PublicBaseURL), "/"),
			RedirectBaseURL:     strings.TrimRight(strings.TrimSpace(raw.HTTP.RedirectBaseURL), "/"),
			RedirectType:        strings.ToLower(strings.TrimSpace(raw.HTTP.RedirectType)),
			MaterializeCacheTTL: materializeCacheTTL,
		},
		P115: P115{
			TokenRefreshAhead:        refreshAhead,
			WorkDirID:                strings.TrimSpace(raw.P115.WorkDirID),
			RequestRate:              raw.P115.RequestRate,
			RequestBurst:             raw.P115.RequestBurst,
			RequestConcurrency:       raw.P115.RequestConcurrency,
			OfflineQuotaMinRemaining: raw.P115.OfflineQuotaMinRemaining,
			OfflinePoll:              offlinePoll,
			OfflinePollMin:           offlinePollMin,
			OfflinePollMax:           offlinePollMax,
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

		Aria2RPCSecret: strings.TrimSpace(os.Getenv("MTS_ARIA2_RPC_SECRET")),
		QBitUsername:   strings.TrimSpace(os.Getenv("MTS_QBITTORRENT_USERNAME")),
		QBitPassword:   strings.TrimSpace(os.Getenv("MTS_QBITTORRENT_PASSWORD")),
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
	if err := c.Rclone.Validate(); err != nil {
		return err
	}
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
	_, port, err := net.SplitHostPort(c.HTTP.Addr)
	if err != nil {
		return errors.New("http.listen_addr 必须为 host:port 或 :port")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return errors.New("http.listen_addr 端口必须在 1–65535 之间")
	}

	if c.Library.STRMDir == "" {
		return errors.New("配置项 library.strm_dir 不能为空")
	}
	switch c.Logging.Level {
	case "", "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("配置项 logging.level 必须是 debug、info、warn 或 error，当前为 %q", c.Logging.Level)
	}
	if c.P115.TokenRefreshAhead < 0 ||
		c.P115.RequestRate < 0 || c.P115.RequestBurst < 0 ||
		c.P115.RequestConcurrency < 0 ||
		c.P115.OfflineQuotaMinRemaining < 0 {
		return errors.New("TOKEN 提前刷新时间、115 请求速率、突发数、并发数和离线额度保护值不能小于 0")
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

func defaultFileConfig() FileConfig {
	var raw FileConfig
	raw.Database.Path = "magnet.db"
	raw.Logging.Level = "info"
	raw.HTTP.ListenAddr = ":8080"
	raw.HTTP.RedirectType = "proxy"
	raw.Rclone = DefaultRclone()
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

// RuntimeSecrets reads 115 tokens only from the file; environment overrides apply to API authentication.
func (c Config) RuntimeSecrets() Secrets {
	secrets := c.Credentials
	secrets.P115RefreshToken, secrets.P115AuthToken = "", ""
	overrides := LoadSecrets()

	if overrides.Aria2RPCSecret != "" {
		secrets.Aria2RPCSecret = overrides.Aria2RPCSecret
	}
	if overrides.QBitUsername != "" {
		secrets.QBitUsername = overrides.QBitUsername
	}
	if overrides.QBitPassword != "" {
		secrets.QBitPassword = overrides.QBitPassword
	}
	return secrets
}
