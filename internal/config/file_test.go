package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureAndSaveFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	if err := EnsureFile(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	if err := EnsureFile(path); err != nil {
		t.Fatal(err)
	}
	current, _ := os.ReadFile(path)
	if string(current) != string(original) {
		t.Fatal("existing file overwritten")
	}
	for _, invalid := range []string{"invalid toml", "[http]\npublic_base_url = 'invalid'", "[http]\nredirect_type = 'invalid'"} {
		if err := SaveFile(path, invalid); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
		current, _ := os.ReadFile(path)
		if string(current) != string(original) {
			t.Fatal("invalid save changed file")
		}
	}
	updated := "# keep this comment\n" + strings.Replace(string(original), "8080", "9090", -1)
	if err := SaveFile(path, updated); err != nil {
		t.Fatal(err)
	}
	current, _ = os.ReadFile(path)
	if string(current) != updated {
		t.Fatal("content not preserved")
	}
	cfg, err = LoadFile(path)
	if err != nil || cfg.HTTP.Addr != ":9090" {
		t.Fatalf("config: %+v, %v", cfg, err)
	}
}

func TestEnsureFileStartupHTTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	initial := InitialHTTP{ListenAddr: ":8081", PublicBaseURL: "http://127.0.0.1:8081"}
	if err := EnsureFile(path, initial); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTP.Addr != initial.ListenAddr || cfg.HTTP.PublicBaseURL != initial.PublicBaseURL {
		t.Fatalf("%+v", cfg.HTTP)
	}
	if err := EnsureFile(path, InitialHTTP{ListenAddr: ":9090"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadFile(path)
	if err != nil || cfg.HTTP.Addr != initial.ListenAddr {
		t.Fatalf("existing file changed: %+v, %v", cfg.HTTP, err)
	}
}

func TestRuntimeSecretsFileAndEnvironment(t *testing.T) {
	for _, key := range []string{"MTS_P115_REFRESH_TOKEN", "MTS_ARIA2_RPC_SECRET", "MTS_QBITTORRENT_USERNAME", "MTS_QBITTORRENT_PASSWORD"} {
		t.Setenv(key, "")
	}
	cfg := Config{Credentials: Secrets{P115RefreshToken: "file-token", Aria2RPCSecret: "file-secret", QBitUsername: "file-user", QBitPassword: "file-password"}}
	if cfg.RuntimeSecrets() != cfg.Credentials {
		t.Fatal("file credentials not used")
	}
	t.Setenv("MTS_P115_REFRESH_TOKEN", "env-token")
	resolved := cfg.RuntimeSecrets()
	if resolved.P115RefreshToken != "env-token" || resolved.QBitPassword != "file-password" {
		t.Fatal("environment precedence incorrect")
	}
}

func TestSettingsRequiredAndOptionalFields(t *testing.T) {
	t.Setenv("MTS_P115_REFRESH_TOKEN", "")
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := EnsureFile(path); err != nil {
		t.Fatal(err)
	}
	settings, _ := ReadSettings(path)
	settings.P115.WorkDirID = "42"
	settings.Credentials.P115RefreshToken = "test-token"
	settings.HTTP.MaterializeCacheTTL = ""
	settings.P115.TokenRefreshAhead = " "
	settings.P115.OfflinePoll = ""
	settings.P115.OfflinePollMin = ""
	settings.P115.OfflinePollMax = ""
	settings.Library.SweepInterval = ""
	settings.Ingest.JobTimeout = ""
	settings.Logging.Level = ""
	if err := SaveSettings(path, settings); err != nil {
		t.Fatal(err)
	}
	saved, err := ReadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	defaults := defaultFileConfig()
	if saved.HTTP.MaterializeCacheTTL != defaults.HTTP.MaterializeCacheTTL || saved.P115.TokenRefreshAhead != defaults.P115.TokenRefreshAhead || saved.P115.OfflinePoll != defaults.P115.OfflinePoll || saved.P115.OfflinePollMin != defaults.P115.OfflinePollMin || saved.P115.OfflinePollMax != defaults.P115.OfflinePollMax || saved.Library.SweepInterval != defaults.Library.SweepInterval || saved.Ingest.JobTimeout != defaults.Ingest.JobTimeout || saved.Logging.Level != defaults.Logging.Level {
		t.Fatal("blank optional fields did not use defaults")
	}
	original, _ := os.ReadFile(path)
	for _, field := range []string{"directory", "strm", "token"} {
		invalid := settings
		switch field {
		case "directory":
			invalid.P115.WorkDirID = " "
		case "strm":
			invalid.Library.STRMDir = ""
		case "token":
			invalid.Credentials.P115RefreshToken = ""
		}
		if err := SaveSettings(path, invalid); err == nil {
			t.Fatalf("missing %s accepted", field)
		}
		current, _ := os.ReadFile(path)
		if string(current) != string(original) {
			t.Fatal("invalid save overwrote file")
		}
	}
}
