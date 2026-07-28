package logging

import (
	"bytes"
	"io"
	"sync"
	"time"
)

const timestampLayout = "2006-01-02 15:04:05.000"

// NewTimestampWriter adds a local timestamp to every line written to dst.
// It is idempotent so callers at different layers can safely use it.
func NewTimestampWriter(dst io.Writer) io.Writer {
	if dst == nil {
		return nil
	}
	if _, ok := dst.(*timestampWriter); ok {
		return dst
	}
	return &timestampWriter{dst: dst, atLineStart: true}
}

type timestampWriter struct {
	mu          sync.Mutex
	dst         io.Writer
	atLineStart bool
}

func (w *timestampWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	var out bytes.Buffer
	for _, b := range p {
		if w.atLineStart && b != '\n' {
			out.WriteByte('[')
			out.WriteString(time.Now().Format(timestampLayout))
			out.WriteString("] ")
			w.atLineStart = false
		}
		out.WriteByte(b)
		if b == '\n' {
			w.atLineStart = true
		}
	}
	if _, err := w.dst.Write(out.Bytes()); err != nil {
		return 0, err
	}
	return len(p), nil
}
