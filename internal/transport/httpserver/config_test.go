package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"magnet-to-strm/internal/config"
	"magnet-to-strm/internal/provider/p115"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigAPI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	handler := WithConfigHooks(http.NotFoundHandler(), path, testTokenHooks())
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
	handler := WithConfigHooks(http.NotFoundHandler(), path, testTokenHooks())
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
	if err != nil || cfg.Credentials.P115RefreshToken != "" || cfg.Credentials.QBitPassword != "test-password-private" {
		t.Fatalf("credentials not saved: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatal("credentials file must be private")
	}
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/config", nil))
	if strings.Contains(get.Body.String(), "test-rpc-private") || strings.Contains(get.Body.String(), "test-password-private") {
		t.Fatal("GET exposes credentials")
	}
	if !strings.Contains(get.Body.String(), `"p115_refresh_token":true`) {
		t.Fatal("missing configured status")
	}
	save(nil)
	cfg, _ = config.LoadFile(path)
	if cfg.Credentials.P115RefreshToken != "" {
		t.Fatal("omitted token was cleared")
	}
	save(map[string]string{"aria2_rpc_secret": ""})
	cfg, _ = config.LoadFile(path)
	if cfg.Credentials.Aria2RPCSecret != "" || cfg.Credentials.QBitPassword == "" {
		t.Fatal("explicit clear should affect only the specified credential")
	}
}

func TestConfigTokenChangesRefreshImmediately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := config.EnsureFile(path); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fail := false
	state := p115.Credential{}
	handler := WithConfigHooks(http.NotFoundHandler(), path, ConfigHooks{
		ReadTokens: func(context.Context) (p115.Credential, error) { return state, nil },
		SaveTokens: func(_ context.Context, _ string, refresh, access *string) (p115.Credential, bool, error) {
			calls++
			if refresh != nil {
				state.RefreshToken = *refresh
			}
			if access != nil {
				state.AccessToken = *access
			}
			if fail {
				return state, false, errors.New("secret must never appear in response")
			}
			return state, true, nil
		},
	})
	settings, _ := config.ReadSettings(path)
	settings.P115.WorkDirID = "42"
	save := func(refresh string) string {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"settings": settings, "credentials": map[string]string{"p115_refresh_token": refresh, "p115_auth_token": "auth-token"}})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/api/v1/config", strings.NewReader(string(body))))
		if response.Code != 200 {
			t.Fatalf("save failed: %s", response.Body.String())
		}
		return response.Body.String()
	}
	if result := save("refresh-token"); !strings.Contains(result, `"token_refreshed":true`) {
		t.Fatal(result)
	}
	save("refresh-token")
	if calls != 2 {
		t.Fatalf("unchanged access token was not checked: %d", calls)
	}
	fail = true
	result := save("replacement-token")
	if calls != 3 || !strings.Contains(result, "token_refresh_error") || strings.Contains(result, "secret must") {
		t.Fatal(result)
	}
	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/config", nil))
	if !strings.Contains(get.Body.String(), "auth-token") || !strings.Contains(get.Body.String(), "replacement-token") {
		t.Fatal("GET must show current database token values")
	}
	content, _ := os.ReadFile(path)
	if strings.Contains(string(content), "auth-token") || strings.Contains(string(content), "replacement-token") {
		t.Fatal("tokens leaked into config file")
	}
}

func testTokenHooks() ConfigHooks {
	state := p115.Credential{}
	return ConfigHooks{
		ReadTokens: func(context.Context) (p115.Credential, error) { return state, nil },
		SaveTokens: func(_ context.Context, _ string, refresh, access *string) (p115.Credential, bool, error) {
			if refresh != nil {
				state.RefreshToken = *refresh
			}
			if access != nil {
				state.AccessToken = *access
			}
			return state, false, nil
		},
	}
}
