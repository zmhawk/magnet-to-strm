package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteSTRMCommand(t *testing.T) {
	root := filepath.Join(t.TempDir(), "strms")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "movie.strm")
	const sha1Value = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := os.WriteFile(
		target,
		[]byte("http://127.0.0.1:8080/redirect/"+sha1Value+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configContent := `
[database]
path = "test.db"

[http]
listen_addr = ":8080"
public_base_url = "http://127.0.0.1:8080"

[library]
strm_dir = "` + root + `"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"rewrite-strm", "-config", configPath,
		"http://127.0.0.1:8080/redirect",
		"http://10.10.0.20:6888/redirect",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run failed: %v\n%s", err, stderr.String())
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content),
		"http://10.10.0.20:6888/redirect/"+sha1Value+"\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if !strings.Contains(stdout.String(), "更新 1 个") {
		t.Fatalf("unexpected output: %q", stdout.String())
	}
}

func TestServeHTTPOptions(t *testing.T) {
	for _, tc := range []struct {
		port           int
		listen, public string
		bad            bool
	}{
		{8081, "", "", false}, {0, "127.0.0.1:9090", "", false},
		{0, "[::1]:9090", "https://media.example.test", false},
		{-1, "", "", true}, {65536, "", "", true}, {8081, ":9090", "", true},
		{0, "bad", "", true}, {0, ":0", "", true}, {0, "", "invalid", true},
	} {
		options, err := serveHTTPOptions(tc.port, tc.listen, tc.public)
		if (err != nil) != tc.bad {
			t.Fatalf("%+v: %v", tc, err)
		}
		if tc.port == 8081 && !tc.bad {
			if options.ListenAddr != ":8081" || options.PublicBaseURL != "http://127.0.0.1:8081" {
				t.Fatalf("%+v", options)
			}
		}
	}
}
