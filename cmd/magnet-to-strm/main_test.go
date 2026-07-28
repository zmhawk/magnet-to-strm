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
