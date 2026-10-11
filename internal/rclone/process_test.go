package rclone

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/webdav"
	"magnet-to-strm/internal/config"
)

// Runs with the actual rclone binary, including in the pinned container image.
func TestRclonePlaybackIntegration(t *testing.T) {
	binary, err := exec.LookPath("rclone")
	if err != nil {
		t.Skip("rclone executable not installed")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "objects", "aa", "bb")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("0123456789abcdef", 4096)
	if err := os.WriteFile(filepath.Join(dir, "movie.mkv"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	dav := &webdav.Handler{Prefix: "/dav", FileSystem: webdav.Dir(root), LockSystem: webdav.NewMemLS()}
	var reads atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			reads.Add(1)
		}
		dav.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cacheDir := t.TempDir()
	settings := config.DefaultRclone()
	settings.CacheDir = cacheDir
	settings.Custom = []string{"--vfs-read-chunk-size=128M", "--low-level-retries 3"}
	if err := config.ValidateRcloneExecutable(ctx, settings); err != nil {
		t.Fatal(err)
	}
	invalid := settings
	invalid.Custom = []string{"--not-a-real-rclone-option=true"}
	if err := config.ValidateRcloneExecutable(ctx, invalid); err == nil {
		t.Fatal("unknown flag accepted")
	}
	process, err := Start(ctx, binary, upstream.URL, filepath.Join(t.TempDir(), "unused"), os.Stderr, settings)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("X-Fallback", "yes"); w.WriteHeader(204) })
	gateway := &Gateway{Next: fallback}
	gateway.SetTarget(process.URL)
	server := httptest.NewServer(gateway)
	defer server.Close()
	request := func(method, path, rangeValue string) (int, string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(method, server.URL+path, nil)
		req.Header.Set("Range", rangeValue)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(body), response.Header
	}
	status, body, _ := request(http.MethodGet, "/objects/aa/bb/movie.mkv", "")
	if status != 200 || body != content {
		t.Fatalf("full playback: status=%d bytes=%d", status, len(body))
	}
	var diskCache bool
	if err := filepath.WalkDir(cacheDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == "movie.mkv" {
			diskCache = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !diskCache {
		t.Fatal("playback did not create a disk VFS cache")
	}
	before := reads.Load()
	status, body, headers := request(http.MethodGet, "/objects/aa/bb/movie.mkv", "bytes=10-19")
	if status != 206 || body != content[10:20] || headers.Get("Content-Range") == "" {
		t.Fatalf("range: %d %q %v", status, body, headers)
	}
	if reads.Load() != before {
		t.Fatal("second playback did not use VFS cache")
	}
	status, body, _ = request(http.MethodHead, "/objects/aa/bb/movie.mkv", "")
	if status != 200 || body != "" {
		t.Fatal("HEAD playback failed")
	}
	_, _, headers = request(http.MethodGet, "/dav/objects/aa/bb/movie.mkv", "")
	if headers.Get("X-Fallback") != "yes" {
		t.Fatal("upstream DAV was routed back through rclone")
	}
	process.Close()
	select {
	case <-process.Done():
	default:
		t.Fatal("rclone child was not reaped")
	}
}

func TestStartFailure(t *testing.T) {
	if _, err := Start(context.Background(), filepath.Join(t.TempDir(), "missing"), "http://127.0.0.1:8080", t.TempDir(), io.Discard); err == nil {
		t.Fatal("missing binary accepted")
	}
}
