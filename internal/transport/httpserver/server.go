package httpserver

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"magnet-to-strm/internal/materialize"
	"magnet-to-strm/internal/webui"
)

type HealthChecker interface {
	Ping() error
}

func NewHandler(
	materializer *materialize.Service,
	health HealthChecker,
	tasks TaskStore,
	controller TaskController,
	aria2Handler http.Handler,
	davHandler http.Handler,
	p115Enabled bool,
	logf func(string, ...any),
	optionalQBit ...http.Handler,
) http.Handler {
	mux := http.NewServeMux()
	if tasks != nil {
		registerAPI(mux, tasks, controller, p115Enabled)
	}
	mux.HandleFunc("/api/", func(writer http.ResponseWriter, _ *http.Request) {
		writeAPIError(writer, http.StatusNotFound, "接口不存在")
	})
	mux.Handle("/jsonrpc", aria2Handler)
	if len(optionalQBit) > 0 && optionalQBit[0] != nil {
		mux.Handle("/api/v2/", optionalQBit[0])
	}
	mux.Handle("/dav", davHandler)
	mux.Handle("/dav/", davHandler)
	mux.Handle("/proxy/", http.NotFoundHandler())
	mux.HandleFunc("/redirect/", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", "GET, HEAD")
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		sha1Value := strings.ToLower(strings.TrimPrefix(request.URL.Path, "/redirect/"))
		if strings.Contains(sha1Value, "/") || !validSHA1(sha1Value) {
			http.Error(writer, "invalid sha1", http.StatusBadRequest)
			return
		}
		query, queryErr := url.ParseQuery(request.URL.RawQuery)
		if queryErr != nil {
			http.Error(writer, "invalid query", http.StatusBadRequest)
			return
		}
		for key := range query {
			if key != "info_hash" {
				http.Error(writer, "unsupported query parameter", http.StatusBadRequest)
				return
			}
		}
		var infoHash string
		if values, ok := query["info_hash"]; ok {
			if len(values) != 1 {
				http.Error(writer, "invalid info_hash", http.StatusBadRequest)
				return
			}
			infoHash = strings.ToLower(strings.TrimSpace(values[0]))
			if !validSHA1(infoHash) {
				http.Error(writer, "invalid info_hash", http.StatusBadRequest)
				return
			}
		}
		redirectURL, err := materializer.RedirectFrom(
			request.Context(), sha1Value, infoHash,
		)
		if errors.Is(err, materialize.ErrAssetNotFound) {
			http.NotFound(writer, request)
			return
		}
		if err != nil {
			if logf != nil {
				if request.Context().Err() != nil {
					logf("等待重定向文件 %s 的请求已取消；后台物化任务不受影响",
						sha1Value)
				} else {
					logf("重定向文件 %s 失败: %v", sha1Value, err)
				}
			}
			http.Error(writer, "failed to prepare file", http.StatusBadGateway)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		http.Redirect(writer, request, redirectURL, http.StatusFound)
	})
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(writer, "ok")
	})
	mux.HandleFunc("/readyz", func(writer http.ResponseWriter, _ *http.Request) {
		if err := health.Ping(); err != nil {
			http.Error(writer, "not ready", http.StatusServiceUnavailable)
			return
		}
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(writer, "ready")
	})
	mux.Handle("/", webui.Handler())
	return mux
}

func validSHA1(value string) bool {
	if len(value) != 40 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
