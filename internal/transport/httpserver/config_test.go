package httpserver

import (
	"encoding/json"
	"magnet-to-strm/internal/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	handler := WithConfig(http.NotFoundHandler(), path)
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/config", nil))
	if get.Code != 200 || !strings.Contains(get.Body.String(), "public_base_url") {
		t.Fatalf("GET: %d %s", get.Code, get.Body.String())
	}
	settings, err := config.ReadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Setup.Required {
		t.Fatal("first startup should require setup")
	}
	settings.HTTP.PublicBaseURL = "http://localhost:9090"
	settings.P115.WorkDirID = "42"
	settings.Credentials.P115RefreshToken = "test-token"
	body, err := json.Marshal(map[string]any{"settings": settings, "credentials": map[string]string{"p115_refresh_token": "test-token"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body, origin string
		status       int
	}{
		{string(body), "", 200},
		{`{"settings":{"http":{"public_base_url":"invalid"}}}`, "", 400},
		{`{"content":""}`, "https://evil.example", 403},
	} {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/config", strings.NewReader(tc.body))
		req.Header.Set("Origin", tc.origin)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestSettingsSaveValidationAndSetup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.EnsureFile(path); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	settings, _ := config.ReadSettings(path)
	settings.P115.WorkDirID = "42"
	settings.Credentials.P115RefreshToken = "test-token"
	settings.P115.OfflinePollMax = "1s"
	if err := config.SaveSettings(path, settings); err == nil {
		t.Fatal("invalid interval accepted")
	}
	current, _ := os.ReadFile(path)
	if string(current) != string(original) {
		t.Fatal("invalid save changed configuration")
	}
	settings.P115.OfflinePollMax = "5m"
	settings.P115.WorkDirID = "42"
	if err := config.SaveSettings(path, settings); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(path)
	if err != nil || cfg.SetupRequired || cfg.P115.WorkDirID != "42" {
		t.Fatalf("saved config: %+v, %v", cfg, err)
	}
}

func TestCredentialsAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.EnsureFile(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	settings, _ := config.ReadSettings(path)
	settings.P115.WorkDirID = "42"
	handler := WithConfig(http.NotFoundHandler(), path)
	save := func(credentials map[string]string) {
		body, _ := json.Marshal(map[string]any{"settings": settings, "credentials": credentials})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/config", strings.NewReader(string(body))))
		if rec.Code != 200 {
			t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
		}
	}
	save(map[string]string{"p115_refresh_token": "test-token-private", "aria2_rpc_secret": "test-rpc-private", "qbittorrent_username": "test-user-private", "qbittorrent_password": "test-password-private"})
	cfg, err := config.LoadFile(path)
	if err != nil || cfg.Credentials.P115RefreshToken != "test-token-private" || cfg.Credentials.QBitPassword != "test-password-private" {
		t.Fatalf("credentials not saved: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("credentials file must be private")
	}
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/config", nil))
	if strings.Contains(get.Body.String(), "-private") {
		t.Fatal("GET exposes credentials")
	}
	if !strings.Contains(get.Body.String(), `"p115_refresh_token":true`) {
		t.Fatal("missing configured status")
	}
	save(nil)
	cfg, _ = config.LoadFile(path)
	if cfg.Credentials.P115RefreshToken != "test-token-private" {
		t.Fatal("omitted token was cleared")
	}
	save(map[string]string{"aria2_rpc_secret": ""})
	cfg, _ = config.LoadFile(path)
	if cfg.Credentials.Aria2RPCSecret != "" || cfg.Credentials.P115RefreshToken == "" || cfg.Credentials.QBitPassword == "" {
		t.Fatal("explicit clear should affect only the specified credential")
	}
}
