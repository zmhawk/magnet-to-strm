package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"magnet-to-strm/internal/provider/p115"
	"magnet-to-strm/internal/storage/sqlite"
	"magnet-to-strm/internal/webui"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"magnet-to-strm/internal/config"
)

// WithConfig exposes only the configuration file selected at startup.
type ConfigHooks struct {
	OnSaved    func()
	ReadTokens func(context.Context) (p115.Credential, error)
	SaveTokens func(context.Context, string, *string, *string) (p115.Credential, bool, error)
}

func WithConfig(next http.Handler, path string, onSaved ...func()) http.Handler {
	hooks := ConfigHooks{}
	if len(onSaved) > 0 {
		hooks.OnSaved = onSaved[0]
	}
	return WithConfigHooks(next, path, hooks)
}

func WithConfigHooks(next http.Handler, path string, hooks ConfigHooks) http.Handler {
	if path == "" {
		return next
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return next
	}
	browser, browserErr := newPathBrowser()
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/filesystem" {
			if browserErr != nil {
				writeAPIError(w, 500, "无法读取服务端目录范围")
				return
			}
			browser.ServeHTTP(w, r)
			return
		}
		if r.URL.Path != "/api/v1/config" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodPut {
			methodNotAllowed(w, "GET, PUT")
			return
		}
		if r.Method == http.MethodPut {
			if origin := r.Header.Get("Origin"); origin != "" {
				parsed, err := url.Parse(origin)
				if err != nil || parsed.Host != r.Host {
					writeAPIError(w, http.StatusForbidden, "不允许跨站修改配置")
					return
				}
			}
			var body struct {
				Settings    config.FileConfig  `json:"settings"`
				Credentials map[string]*string `json:"credentials"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&body); err != nil {
				writeAPIError(w, http.StatusBadRequest, "无效的配置请求")
				return
			}
			mu.Lock()
			defer mu.Unlock()
			stored, err := config.ReadSettings(absolute)
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "读取现有凭据失败")
				return
			}
			// The listener is managed at startup, never through the Web UI.
			body.Settings.HTTP.ListenAddr = stored.HTTP.ListenAddr
			body.Settings.Credentials = stored.Credentials
			for key, value := range body.Credentials {
				if value == nil {
					continue
				}
				switch key {
				case "p115_auth_token":
					// 115 tokens are stored only in SQLite.
				case "p115_refresh_token":
					// Handled by the token store below.
				case "aria2_rpc_secret":
					body.Settings.Credentials.Aria2RPCSecret = *value
				case "qbittorrent_username":
					body.Settings.Credentials.QBitUsername = *value
				case "qbittorrent_password":
					body.Settings.Credentials.QBitPassword = *value
				default:
					writeAPIError(w, http.StatusBadRequest, "未知凭据字段")
					return
				}
			}
			if browserErr != nil {
				writeAPIError(w, 500, "无法读取服务端目录范围")
				return
			}
			database, err := browser.selection(body.Settings.Database.Path, true)
			if err != nil {
				writeAPIError(w, 400, "数据库路径："+err.Error())
				return
			}
			strm, err := browser.selection(body.Settings.Library.STRMDir, false)
			if err != nil {
				writeAPIError(w, 400, "STRM 输出目录："+err.Error())
				return
			}
			body.Settings.Database.Path = database
			body.Settings.Library.STRMDir = strm

			existingTokens, err := readTokens(r.Context(), stored, hooks)
			if err != nil {
				writeAPIError(w, 500, "读取 115 TOKEN 失败")
				return
			}
			refresh, access := body.Credentials["p115_refresh_token"], body.Credentials["p115_auth_token"]
			storedDatabase, _ := filepath.Abs(stored.Database.Path)
			if database != storedDatabase {
				if refresh == nil {
					refresh = &existingTokens.RefreshToken
				}
				if access == nil {
					access = &existingTokens.AccessToken
				}
			}
			requiredRefresh := existingTokens.RefreshToken
			if refresh != nil {
				requiredRefresh = strings.TrimSpace(*refresh)
			}
			if requiredRefresh == "" {
				writeAPIError(w, 400, "请填写 115 Refresh TOKEN")
				return
			}
			if err := config.ValidateRcloneExecutable(r.Context(), body.Settings.Rclone); err != nil {
				writeAPIError(w, 400, err.Error())
				return
			}
			if err := config.SaveSettings(absolute, body.Settings); err != nil {
				writeAPIError(w, 400, err.Error())
				return
			}
			tokenCtx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			var tokens p115.Credential
			var refreshed bool
			if hooks.SaveTokens != nil {
				tokens, refreshed, err = hooks.SaveTokens(tokenCtx, database, refresh, access)
			} else {
				tokens, refreshed, err = SaveDatabaseTokens(tokenCtx, body.Settings, refresh, access)
			}
			cancel()
			refreshError := ""
			if err != nil {
				refreshError = "配置已保存，但 115 TOKEN 验证或刷新失败，请检查凭据后重新保存"
			}
			savedSettings, _ := config.ReadSettings(absolute)
			stored.Credentials.P115RefreshToken, stored.Credentials.P115AuthToken = "", ""
			settingsChanged := !reflect.DeepEqual(stored, savedSettings)
			autoRestart := hooks.OnSaved != nil
			writeJSON(w, 200, map[string]any{"path": absolute, "restart_required": !autoRestart && settingsChanged, "restarting": autoRestart, "restart_url": body.Settings.HTTP.PublicBaseURL, "token_refreshed": refreshed, "token_refresh_error": refreshError, "credentials_values": tokenValues(tokens)})
			if hooks.OnSaved != nil {
				hooks.OnSaved()
			}

			return
		}
		mu.Lock()
		defer mu.Unlock()
		if err := config.EnsureFile(absolute); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "创建配置文件失败")
			return
		}
		settings, err := config.ReadSettings(absolute)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "读取配置文件失败")
			return
		}
		tokens, err := readTokens(r.Context(), settings, hooks)
		if err != nil {
			writeAPIError(w, 500, "读取 115 TOKEN 失败")
			return
		}
		effective := (config.Config{Credentials: settings.Credentials}).RuntimeSecrets()
		writeJSON(w, http.StatusOK, map[string]any{"path": absolute, "settings": settings, "credentials_values": tokenValues(tokens), "setup_required": settings.Setup.Required, "setup_mode": hooks.OnSaved != nil, "credentials_configured": map[string]bool{
			"p115_refresh_token":   tokens.RefreshToken != "",
			"p115_auth_token":      tokens.AccessToken != "",
			"aria2_rpc_secret":     effective.Aria2RPCSecret != "",
			"qbittorrent_username": effective.QBitUsername != "",
			"qbittorrent_password": effective.QBitPassword != "",
		}})
	})
}

// SetupHandler exposes configuration and assets only, without opening application storage.
func SetupHandler(path string, onSaved func(), options ...ConfigHooks) http.Handler {
	assets := webui.Handler()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/jsonrpc" || strings.HasPrefix(r.URL.Path, "/dav") || strings.HasPrefix(r.URL.Path, "/objects/") || strings.HasPrefix(r.URL.Path, "/redirect/") {
			writeAPIError(w, http.StatusServiceUnavailable, "请先完成初始化配置")
			return
		}
		assets.ServeHTTP(w, r)
	})
	hooks := ConfigHooks{OnSaved: onSaved}
	if len(options) > 0 {
		hooks = options[0]
		hooks.OnSaved = onSaved
	}
	return WithConfigHooks(next, path, hooks)
}

func tokenValues(tokens p115.Credential) map[string]string {
	return map[string]string{"p115_refresh_token": tokens.RefreshToken, "p115_auth_token": tokens.AccessToken}
}
func readTokens(ctx context.Context, settings config.FileConfig, hooks ConfigHooks) (p115.Credential, error) {
	if hooks.ReadTokens != nil {
		return hooks.ReadTokens(ctx)
	}
	if _, err := os.Stat(settings.Database.Path); errors.Is(err, os.ErrNotExist) {
		return p115.Credential{}, nil
	} else if err != nil {
		return p115.Credential{}, err
	}
	db, err := sqlite.Open(settings.Database.Path)
	if err != nil {
		return p115.Credential{}, err
	}
	defer db.Close()
	return p115.NewTokenEditor(db, config.P115{}, nil).Tokens(ctx)
}
func SaveDatabaseTokens(ctx context.Context, settings config.FileConfig, refresh, access *string) (p115.Credential, bool, error) {
	db, err := sqlite.Open(settings.Database.Path)
	if err != nil {
		return p115.Credential{}, false, err
	}
	defer db.Close()
	return p115.NewTokenEditor(db, config.P115{}, nil).UpdateTokens(ctx, refresh, access)
}
