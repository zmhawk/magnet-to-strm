package logging

import (
	"bytes"
	"strings"
	"testing"
)

func TestLoggerFiltersLegacyMessagesByLevel(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(&output, "info")
	if err != nil {
		t.Fatal(err)
	}
	logger.Printf("115 API: GET https://example.test")
	logger.Printf("等待重定向文件 abc 的请求已取消；后台物化任务不受影响")
	logger.Printf("任务已完成")
	logger.Printf("警告：稍后重试")

	got := output.String()
	if strings.Contains(got, "115 API") || strings.Contains(got, "请求已取消") {
		t.Fatalf("debug message was logged at info level: %q", got)
	}
	if !strings.Contains(got, "任务已完成") || !strings.Contains(got, "警告：稍后重试") {
		t.Fatalf("info or warning message missing: %q", got)
	}
}

func TestLoggerDebugLevelIncludesAPIRequests(t *testing.T) {
	var output bytes.Buffer
	logger, err := New(&output, "debug")
	if err != nil {
		t.Fatal(err)
	}
	logger.Writer().Write([]byte("115 API: GET https://example.test\n"))
	if !strings.Contains(output.String(), "115 API: GET") {
		t.Fatalf("debug message missing: %q", output.String())
	}
}

func TestLoggerRejectsUnknownLevel(t *testing.T) {
	if _, err := New(&bytes.Buffer{}, "verbose"); err == nil {
		t.Fatal("unknown log level was accepted")
	}
}
