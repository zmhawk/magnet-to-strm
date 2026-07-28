package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadUsesNamespacedConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
[database]
path = "/tmp/mts.db"

[http]
listen_addr = ":9090"
public_base_url = "https://media.example.test/"
redirect_base_url = "https://files.example.test/content/"
redirect_type = "stable_dav"
materialize_cache_ttl = "15m"

[p115]
work_dir_id = "42"
request_rate = 1.5
request_burst = 3
request_concurrency = 4
offline_poll_min_interval = "3s"
offline_poll_max_interval = "4m"

[library]
strm_dir = "/tmp/strms"
cache_retention = "48h"
cache_max_size = "4TB"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MTS_DB_PATH", "/should/not/be/read.db")
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Path != "/tmp/mts.db" || cfg.HTTP.Addr != ":9090" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.HTTP.PublicBaseURL != "https://media.example.test" {
		t.Fatalf("public URL was not normalized: %q", cfg.HTTP.PublicBaseURL)
	}
	if cfg.HTTP.RedirectType != "stable_dav" ||
		cfg.HTTP.MaterializeCacheTTL != 15*time.Minute {
		t.Fatalf("unexpected redirect configuration: %+v", cfg.HTTP)
	}
	if cfg.Library.CacheRetention != 48*time.Hour {
		t.Fatalf("unexpected retention: %s", cfg.Library.CacheRetention)
	}
	if cfg.Library.CacheMaxSizeBytes != 4*(1<<40) {
		t.Fatalf("unexpected cache max size: %d", cfg.Library.CacheMaxSizeBytes)
	}
	if cfg.Library.STRMDir != "/tmp/strms" {
		t.Fatalf("unexpected STRM directory: %q", cfg.Library.STRMDir)
	}
	if cfg.P115.RequestRate != 1.5 || cfg.P115.RequestBurst != 3 ||
		cfg.P115.RequestConcurrency != 4 ||
		cfg.P115.OfflinePollMin != 3*time.Second ||
		cfg.P115.OfflinePollMax != 4*time.Minute {
		t.Fatalf("unexpected 115 request limiter: %+v", cfg.P115)
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentOnlyLoadsSecrets(t *testing.T) {
	t.Setenv("MTS_P115_REFRESH_TOKEN", "refresh")
	t.Setenv("MTS_ARIA2_RPC_SECRET", "rpc")
	secrets := LoadSecrets()
	if secrets.P115RefreshToken != "refresh" || secrets.Aria2RPCSecret != "rpc" {
		t.Fatalf("unexpected secrets: %+v", secrets)
	}
}

func TestUnknownConfigFieldIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[database]\nunknown = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestStableDAVDoesNotRequireUpstreamBaseURL(t *testing.T) {
	cfg := Config{
		Database: Database{Path: "test.db"},
		HTTP: HTTP{
			Addr: ":8080", PublicBaseURL: "https://media.test",
			RedirectBaseURL: "https://rclone.test", RedirectType: "stable_dav",
			MaterializeCacheTTL: time.Minute,
		},
		P115: P115{
			WorkDirID: "work", OfflinePoll: time.Second,
		},
		Library: Library{
			STRMDir: "strms", CacheRetention: time.Hour, SweepInterval: time.Minute,
		},
		Ingest: Ingest{JobTimeout: time.Hour},
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("stable DAV unexpectedly requires an upstream URL: %v", err)
	}
}

func TestServeAllowsMissing115WorkDir(t *testing.T) {
	cfg := Config{
		Database: Database{Path: "test.db"},
		HTTP: HTTP{
			Addr: ":8080", PublicBaseURL: "https://media.test",
			RedirectBaseURL: "https://files.test", RedirectType: "direct",
			MaterializeCacheTTL: time.Minute,
		},
		P115: P115{OfflinePoll: time.Second},
		Library: Library{
			STRMDir: "strms", CacheRetention: time.Hour, SweepInterval: time.Minute,
		},
		Ingest: Ingest{JobTimeout: time.Hour},
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe() rejected local-only mode: %v", err)
	}
	if err := cfg.ValidateAdd(); err == nil {
		t.Fatal("ValidateAdd() accepted missing 115 work directory")
	}
}

func TestLoadDotEnvSupportsCROnlyLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(
		"MTS_P115_REFRESH_TOKEN=one\rMTS_ARIA2_RPC_SECRET='two'\r",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MTS_P115_REFRESH_TOKEN", "")
	if err := os.Unsetenv("MTS_P115_REFRESH_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("MTS_ARIA2_RPC_SECRET"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Unsetenv("MTS_P115_REFRESH_TOKEN")
		_ = os.Unsetenv("MTS_ARIA2_RPC_SECRET")
	})
	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("MTS_P115_REFRESH_TOKEN") != "one" ||
		os.Getenv("MTS_ARIA2_RPC_SECRET") != "two" {
		t.Fatalf("dotenv values not loaded")
	}
}

func TestDotEnvRejectsNonSecretConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("MTS_DB_PATH=wrong.db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(path); err == nil {
		t.Fatal("expected non-secret environment variable to be rejected")
	}
}
