package rclone

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
)

// Gateway proxies only stable object reads; the upstream DAV and all other
// application routes remain separate to avoid a playback proxy loop.
type Gateway struct {
	Next   http.Handler
	target atomic.Pointer[httputil.ReverseProxy]
}

func (g *Gateway) SetTarget(target *url.URL) {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "内置 rclone 播放代理不可用", http.StatusBadGateway)
	}
	g.target.Store(proxy)
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	proxy := g.target.Load()
	if proxy != nil && strings.HasPrefix(r.URL.Path, "/objects/") && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		proxy.ServeHTTP(w, r)
		return
	}
	g.Next.ServeHTTP(w, r)
}
