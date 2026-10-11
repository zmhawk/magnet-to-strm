package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"magnet-to-strm/internal/config"
)

func TestSetupServerOnlyAndRestart(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(home, ".mts-setup-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	path := filepath.Join(root, "config.toml")
	if err := config.EnsureFile(path, config.InitialHTTP{ListenAddr: addr, PublicBaseURL: "http://" + addr}); err != nil {
		t.Fatal(err)
	}
	settings, err := config.ReadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	settings.Database.Path = filepath.Join(root, "magnet.db")
	settings.Library.STRMDir = filepath.Join(root, "strms")
	cfg, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		restart, err := runSetupServer(cfg, io.Discard)
		if err == nil && !restart {
			err = io.ErrUnexpectedEOF
		}
		done <- err
	}()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	base := "http://" + addr
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err := client.Get(base + "/api/v1/config")
		if err == nil {
			response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, route := range []string{"/api/v1/jobs", "/jsonrpc", "/dav/", "/objects/movie.mkv", "/redirect/test"} {
		response, err := client.Get(base + route)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("business route enabled: %s %d", route, response.StatusCode)
		}
	}
	for _, route := range []string{"/", "/api/v1/filesystem?kind=directory"} {
		response, err := client.Get(base + route)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("setup route unavailable: %s", route)
		}
	}
	settings.P115.WorkDirID = "42"
	body, _ := json.Marshal(map[string]any{"settings": settings, "credentials": map[string]string{"p115_refresh_token": "test-token"}})
	request, _ := http.NewRequest(http.MethodPut, base+"/api/v1/config", bytes.NewReader(body))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Restarting bool `json:"restarting"`
	}
	err = json.NewDecoder(response.Body).Decode(&result)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !result.Restarting {
		t.Fatalf("save response: %+v %v", result, err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("setup server did not restart")
	}
	for _, item := range []string{settings.Database.Path, settings.Library.STRMDir, filepath.Join(root, "rclone-cache")} {
		if _, err := os.Stat(item); !os.IsNotExist(err) {
			t.Fatalf("setup created application storage: %s", item)
		}
	}
	saved, err := config.ReadSettings(path)
	if err != nil || saved.Setup.Required {
		t.Fatalf("setup state was not completed: %v", err)
	}
}
