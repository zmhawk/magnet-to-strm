package config

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var rcloneOptionName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ParseRcloneOption accepts one complete CLI option per line. It never invokes a shell.
func ParseRcloneOption(line string) (name, value string, err error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", nil
	}
	if !strings.HasPrefix(line, "--") {
		return "", "", fmt.Errorf("rclone 参数应以 -- 开头: %q", line)
	}
	text := strings.TrimPrefix(line, "--")
	split := strings.IndexAny(text, "= \t")
	value = "true"
	name = text
	if split >= 0 {
		name, value = text[:split], strings.TrimSpace(text[split+1:])
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			quote := value[0]
			if len(value) < 2 || value[len(value)-1] != quote {
				return "", "", fmt.Errorf("rclone 参数引号未闭合: %q", line)
			}
			value = value[1 : len(value)-1]
		}
	}
	if !rcloneOptionName.MatchString(name) {
		return "", "", fmt.Errorf("rclone 参数名无效: %q", name)
	}
	return name, value, nil
}

func RcloneServeOption(name string) bool {
	return strings.HasPrefix(name, "vfs-") || strings.HasPrefix(name, "server-") || name == "dir-cache-time" || name == "poll-interval"
}

func (r Rclone) Validate() error {
	r = r.WithDefaults()
	switch r.VFSCacheMode {
	case "off", "minimal", "writes", "full":
	default:
		return fmt.Errorf("rclone.vfs_cache_mode 无效: %s", r.VFSCacheMode)
	}
	for key, value := range map[string]string{"server_read_timeout": r.ServerReadTimeout, "server_write_timeout": r.ServerWriteTimeout, "vfs_cache_max_age": r.VFSCacheMaxAge, "vfs_cache_poll_interval": r.VFSCachePollInterval} {
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return fmt.Errorf("rclone.%s 必须是正数时间，例如 24h 或 5m", key)
		}
	}
	for key, value := range map[string]string{"vfs_cache_max_size": r.VFSCacheMaxSize, "vfs_read_ahead": r.VFSReadAhead, "buffer_size": r.BufferSize, "max_buffer_memory": r.MaxBufferMemory} {
		size, err := parseByteSize("rclone."+key, value)
		if err != nil || size < 0 {
			return fmt.Errorf("rclone.%s 容量无效: %s", key, value)
		}
	}

	seen := make(map[string]bool)
	for _, line := range r.Custom {
		name, _, err := ParseRcloneOption(line)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		switch name {
		case "addr", "fs", "type", "read-only", "config", "cache-dir", "server-read-timeout", "server-write-timeout", "vfs-cache-mode", "vfs-cache-max-size", "vfs-cache-max-age", "vfs-cache-poll-interval", "vfs-read-ahead", "buffer-size", "max-buffer-memory":
			return fmt.Errorf("rclone 参数 %s 请使用对应表单字段设置", name)
		}
		if name == "rc" || strings.HasPrefix(name, "rc-") {
			return fmt.Errorf("rclone 参数 %s 由内置服务管理", name)
		}
		if seen[name] {
			return fmt.Errorf("rclone 参数重复: %s", name)
		}
		seen[name] = true
	}
	return nil
}

func (r Rclone) WithDefaults() Rclone {
	d := DefaultRclone()
	if strings.TrimSpace(r.CacheDir) == "" {
		r.CacheDir = d.CacheDir
	}
	if strings.TrimSpace(r.ServerReadTimeout) == "" {
		r.ServerReadTimeout = d.ServerReadTimeout
	}
	if strings.TrimSpace(r.ServerWriteTimeout) == "" {
		r.ServerWriteTimeout = d.ServerWriteTimeout
	}
	if strings.TrimSpace(r.VFSCacheMode) == "" {
		r.VFSCacheMode = d.VFSCacheMode
	}
	if strings.TrimSpace(r.VFSCacheMaxSize) == "" {
		r.VFSCacheMaxSize = d.VFSCacheMaxSize
	}
	if strings.TrimSpace(r.VFSCacheMaxAge) == "" {
		r.VFSCacheMaxAge = d.VFSCacheMaxAge
	}
	if strings.TrimSpace(r.VFSCachePollInterval) == "" {
		r.VFSCachePollInterval = d.VFSCachePollInterval
	}
	if strings.TrimSpace(r.VFSReadAhead) == "" {
		r.VFSReadAhead = d.VFSReadAhead
	}
	if strings.TrimSpace(r.BufferSize) == "" {
		r.BufferSize = d.BufferSize
	}
	if strings.TrimSpace(r.MaxBufferMemory) == "" {
		r.MaxBufferMemory = d.MaxBufferMemory
	}
	return r
}

func (r Rclone) ServeOptions() map[string]any {
	r = r.WithDefaults()
	return map[string]any{
		"server_read_timeout":     r.ServerReadTimeout,
		"server_write_timeout":    r.ServerWriteTimeout,
		"vfs_cache_mode":          r.VFSCacheMode,
		"vfs_cache_max_size":      r.VFSCacheMaxSize,
		"vfs_cache_max_age":       r.VFSCacheMaxAge,
		"vfs_cache_poll_interval": r.VFSCachePollInterval,
		"vfs_read_ahead":          r.VFSReadAhead,
	}
}

// ValidateRcloneExecutable checks option names and values with rclone's own flag
// parser. Help exits without starting RC/VFS servers or accessing the remote.
func ValidateRcloneExecutable(ctx context.Context, settings Rclone) error {
	if !settings.Enabled {
		return nil
	}
	binary, err := exec.LookPath("rclone")
	if errors.Is(err, exec.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	settings = settings.WithDefaults()
	args := []string{"serve", "http", "--help", "--buffer-size=" + settings.BufferSize, "--max-buffer-memory=" + settings.MaxBufferMemory}
	for key, value := range settings.ServeOptions() {
		args = append(args, "--"+strings.ReplaceAll(key, "_", "-")+"="+value.(string))
	}
	for _, line := range settings.Custom {
		name, value, err := ParseRcloneOption(line)
		if err != nil {
			return err
		}
		if name != "" {
			args = append(args, "--"+name+"="+value)
		}
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(checkCtx, binary, args...).CombinedOutput()
	if err != nil {
		message := strings.SplitN(strings.TrimSpace(string(output)), "\n", 2)[0]
		return fmt.Errorf("rclone 参数校验失败: %s", message)
	}
	return nil
}
