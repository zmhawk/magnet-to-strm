package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// EnsureFile creates a usable local-mode configuration without replacing an existing file.
type InitialHTTP struct {
	ListenAddr    string
	PublicBaseURL string
}

func EnsureFile(path string, initial ...InitialHTTP) error {
	raw := defaultFileConfig()
	raw.Setup.Required = true
	raw.HTTP.PublicBaseURL = "http://127.0.0.1:8080"
	raw.Library.STRMDir = "strms"
	if len(initial) > 0 {
		if initial[0].ListenAddr != "" {
			raw.HTTP.ListenAddr = initial[0].ListenAddr
		}
		if initial[0].PublicBaseURL != "" {
			raw.HTTP.PublicBaseURL = initial[0].PublicBaseURL
		}
	}
	raw.HTTP.RedirectBaseURL = raw.HTTP.PublicBaseURL
	content, err := toml.Marshal(raw)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("创建配置文件: %w", err)
	}
	_, writeErr := file.Write(content)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}

// SaveFile validates before atomically replacing the selected configuration file.
func SaveFile(path, content string) error {
	cfg, err := Parse(strings.NewReader(content))
	if err != nil {
		return err
	}
	if err := cfg.ValidateServe(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if cfg.Credentials != (Secrets{}) {
		mode = 0o600
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if _, err := file.WriteString(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// ReadSettings returns editable values with defaults for omitted fields.
func ReadSettings(path string) (FileConfig, error) {
	raw := defaultFileConfig()
	content, err := os.ReadFile(path)
	if err != nil {
		return raw, err
	}
	err = toml.Unmarshal(content, &raw)
	if raw.HTTP.RedirectType == "stable_dav" {
		raw.HTTP.RedirectType = "proxy"
	}
	if strings.TrimSpace(raw.HTTP.RedirectBaseURL) == "" {
		raw.HTTP.RedirectBaseURL = raw.HTTP.PublicBaseURL
	}
	return raw, err
}

func SaveSettings(path string, settings FileConfig) error {
	if strings.TrimSpace(settings.P115.WorkDirID) == "" {
		return errors.New("请填写 115 工作目录 ID")
	}
	if strings.TrimSpace(settings.Library.STRMDir) == "" {
		return errors.New("请填写 STRM 输出目录")
	}
	if strings.TrimSpace((Config{Credentials: settings.Credentials}).RuntimeSecrets().P115RefreshToken) == "" {
		return errors.New("请填写 115 Refresh TOKEN")
	}
	defaults := defaultFileConfig()
	for _, field := range []struct {
		value    *string
		fallback string
	}{
		{&settings.HTTP.MaterializeCacheTTL, defaults.HTTP.MaterializeCacheTTL},
		{&settings.P115.TokenRefreshAhead, defaults.P115.TokenRefreshAhead},
		{&settings.P115.OfflinePoll, defaults.P115.OfflinePoll},
		{&settings.P115.OfflinePollMin, defaults.P115.OfflinePollMin},
		{&settings.P115.OfflinePollMax, defaults.P115.OfflinePollMax},
		{&settings.Library.SweepInterval, defaults.Library.SweepInterval},
		{&settings.Ingest.JobTimeout, defaults.Ingest.JobTimeout},
		{&settings.Logging.Level, defaults.Logging.Level},
	} {
		if strings.TrimSpace(*field.value) == "" {
			*field.value = field.fallback
		}
	}
	settings.Rclone = settings.Rclone.WithDefaults()
	settings.Setup.Required = false
	content, err := toml.Marshal(settings)
	if err != nil {
		return err
	}
	return SaveFile(path, string(content))
}
