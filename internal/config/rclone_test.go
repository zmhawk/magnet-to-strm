package config

import "testing"

func TestRcloneCustomValidation(t *testing.T) {
	for _, options := range [][]string{
		{"--rc-no-auth"}, {"--vfs-cache-mode=off"},
		{"--vfs-read-chunk-size=128M", "--vfs-read-chunk-size 64M"},
		{"retries=3"}, {"--invalid$name=1"}, {`--user="unclosed`},
	} {
		r := DefaultRclone()
		r.Custom = options
		if err := r.Validate(); err == nil {
			t.Fatalf("accepted invalid options: %+v", options)
		}
	}
	r := DefaultRclone()
	r.Custom = []string{"--vfs-read-chunk-size=128M", "--retries 3", ""}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestParseRcloneOption(t *testing.T) {
	for _, test := range []struct{ line, name, value string }{
		{"--vfs-read-chunk-size=128M", "vfs-read-chunk-size", "128M"},
		{" --retries 3 ", "retries", "3"},
		{"--use-mmap", "use-mmap", "true"},
		{`--user-agent="Media Player"`, "user-agent", "Media Player"},
		{"--user-agent 'Media Player'", "user-agent", "Media Player"},
		{"--header=X-Test=a=b", "header", "X-Test=a=b"},
		{"", "", ""},
	} {
		name, value, err := ParseRcloneOption(test.line)
		if err != nil || name != test.name || value != test.value {
			t.Fatalf("%q: %q %q %v", test.line, name, value, err)
		}
	}
}

func TestRcloneCommonValuesAreValidated(t *testing.T) {
	for _, change := range []func(*Rclone){
		func(r *Rclone) { r.VFSCacheMode = "invalid-mode" },
		func(r *Rclone) { r.ServerReadTimeout = "not-a-time" },
		func(r *Rclone) { r.VFSCachePollInterval = "-5m" },
		func(r *Rclone) { r.BufferSize = "not-a-size" },
	} {
		value := DefaultRclone()
		change(&value)
		if err := value.Validate(); err == nil {
			t.Fatal("invalid rclone value accepted")
		}
	}
}
