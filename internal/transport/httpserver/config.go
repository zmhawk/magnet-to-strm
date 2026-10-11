package httpserver

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"sync"

	"magnet-to-strm/internal/config"
)

// WithConfig exposes only the configuration file selected at startup.
func WithConfig(next http.Handler, path string) http.Handler {
	if path == "" {
		return next
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return next
	}
	var mu sync.Mutex
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			body.Settings.Credentials = stored.Credentials
			for key, value := range body.Credentials {
				if value == nil {
					continue
				}
				switch key {
				case "p115_refresh_token":
					body.Settings.Credentials.P115RefreshToken = *value
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
			if err := config.SaveSettings(absolute, body.Settings); err != nil {
				writeAPIError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"path": absolute, "restart_required": true})
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
		effective := (config.Config{Credentials: settings.Credentials}).RuntimeSecrets()
		writeJSON(w, http.StatusOK, map[string]any{"path": absolute, "settings": settings, "setup_required": settings.Setup.Required, "credentials_configured": map[string]bool{
			"p115_refresh_token":   effective.P115RefreshToken != "",
			"aria2_rpc_secret":     effective.Aria2RPCSecret != "",
			"qbittorrent_username": effective.QBitUsername != "",
			"qbittorrent_password": effective.QBitPassword != "",
		}})
	})
}
