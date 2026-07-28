package logging

import (
	"bytes"
	"regexp"
	"testing"
)

func TestTimestampWriterPrefixesEveryLine(t *testing.T) {
	var dst bytes.Buffer
	w := NewTimestampWriter(&dst)
	if _, err := w.Write([]byte("first\nsecond\n")); err != nil {
		t.Fatal(err)
	}

	pattern := regexp.MustCompile(`^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}\] (first|second)\n$`)
	lines := bytes.SplitAfter(dst.Bytes(), []byte{'\n'})
	if len(lines) != 3 || !pattern.Match(lines[0]) || !pattern.Match(lines[1]) {
		t.Fatalf("unexpected timestamped output: %q", dst.String())
	}
}

func TestTimestampWriterIsIdempotent(t *testing.T) {
	var dst bytes.Buffer
	w := NewTimestampWriter(&dst)
	if got := NewTimestampWriter(w); got != w {
		t.Fatal("wrapping a timestamp writer should be idempotent")
	}
}
