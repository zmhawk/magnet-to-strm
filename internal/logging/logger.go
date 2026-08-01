package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Logger is a leveled logger backed by the standard library's log/slog.
// Messages keep the application's existing human-readable format.
type Logger struct {
	logger *slog.Logger
}

func New(dst io.Writer, level string) (*Logger, error) {
	if strings.TrimSpace(level) == "" {
		level = "info"
	}
	if dst == nil {
		dst = io.Discard
	}
	var minimum slog.Level
	if err := minimum.UnmarshalText([]byte(strings.ToUpper(strings.TrimSpace(level)))); err != nil {
		return nil, fmt.Errorf("无效日志级别 %q: %w", level, err)
	}
	handler := &messageHandler{
		dst:   NewTimestampWriter(dst),
		level: minimum,
	}
	return &Logger{logger: slog.New(handler)}, nil
}

// Printf is compatible with the existing Logf callbacks. It assigns levels to
// legacy messages while call sites are gradually migrated to structured logs.
func (l *Logger) Printf(format string, values ...any) {
	if l == nil {
		return
	}
	message := fmt.Sprintf(format, values...)
	l.logger.Log(context.Background(), levelForMessage(message), message)
}

// Writer adapts line-oriented legacy output to the configured logger.
func (l *Logger) Writer() io.Writer {
	return loggerWriter{logger: l}
}

func levelForMessage(message string) slog.Level {
	trimmed := strings.TrimSpace(message)
	switch {
	case strings.HasPrefix(trimmed, "115 API:"),
		strings.Contains(trimmed, "请求已取消；后台物化任务不受影响"):
		return slog.LevelDebug
	case strings.HasPrefix(trimmed, "警告"),
		strings.Contains(trimmed, "失败"),
		strings.Contains(trimmed, "无法"),
		strings.Contains(trimmed, "拒绝"):
		return slog.LevelWarn
	case strings.HasPrefix(trimmed, "错误"):
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type loggerWriter struct {
	logger *Logger
}

func (w loggerWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimSuffix(string(p), "\n"), "\n") {
		if line != "" {
			w.logger.Printf("%s", line)
		}
	}
	return len(p), nil
}

type messageHandler struct {
	dst   io.Writer
	level slog.Level
}

func (h *messageHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *messageHandler) Handle(_ context.Context, record slog.Record) error {
	_, err := fmt.Fprintln(h.dst, record.Message)
	return err
}

func (h *messageHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *messageHandler) WithGroup(_ string) slog.Handler      { return h }
