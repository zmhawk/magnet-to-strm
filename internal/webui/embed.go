package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dist
var assets embed.FS

func Handler() http.Handler {
	content, err := fs.Sub(assets, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(content))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path := strings.TrimPrefix(request.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(content, path); err != nil {
			clone := request.Clone(request.Context())
			clone.URL.Path = "/"
			files.ServeHTTP(writer, clone)
			return
		}
		files.ServeHTTP(writer, request)
	})
}
