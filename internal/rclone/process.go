package rclone

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"magnet-to-strm/internal/config"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Process struct {
	command *exec.Cmd
	done    chan struct{}
	err     error
	URL     *url.URL
}

func (p *Process) Done() <-chan struct{} { return p.done }
func (p *Process) Err() error            { <-p.done; return p.err }
func (p *Process) Close() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.command.Process.Signal(os.Interrupt)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		_ = p.command.Process.Kill()
		<-p.done
	}
}

func freeAddress() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer listener.Close()
	return listener.Addr().String(), nil
}

// Start supervises rcd and creates its HTTP/VFS server through the authenticated
// loopback RC API. The application HTTP server must already be listening.
func Start(ctx context.Context, binary, upstream, cacheDir string, output io.Writer, options ...config.Rclone) (*Process, error) {
	settings := config.DefaultRclone()
	if len(options) > 0 {
		settings = options[0].WithDefaults()
	}
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	if settings.CacheDir != "" {
		cacheDir = settings.CacheDir
	}
	addr, err := freeAddress()
	if err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	password := hex.EncodeToString(secret)
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return nil, err
	}
	cacheDir, err = filepath.Abs(cacheDir)
	if err != nil {
		return nil, err
	}
	args := []string{"rcd", "--rc-addr=" + addr, "--config=" + os.DevNull, "--cache-dir=" + cacheDir, "--log-level=INFO", "--buffer-size=" + settings.BufferSize, "--max-buffer-memory=" + settings.MaxBufferMemory}
	for _, line := range settings.Custom {
		name, value, _ := config.ParseRcloneOption(line)
		if name != "" && !config.RcloneServeOption(name) {
			args = append(args, "--"+name+"="+value)
		}
	}
	command := exec.Command(binary, args...)
	command.Env = append(os.Environ(),
		"RCLONE_RC_USER=magnet-to-strm", "RCLONE_RC_PASS="+password,
		"RCLONE_CONFIG_MTS_TYPE=webdav", "RCLONE_CONFIG_MTS_URL="+upstream+"/dav", "RCLONE_CONFIG_MTS_VENDOR=other",
	)
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		return nil, err
	}
	process := &Process{command: command, done: make(chan struct{})}
	go func() { process.err = command.Wait(); close(process.done) }()
	success := false
	defer func() {
		if !success {
			process.Close()
		}
	}()
	startup, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	call := func(endpoint string, params any, result any) error {
		data, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(startup, http.MethodPost, "http://"+addr+"/"+endpoint, bytes.NewReader(data))
		if err != nil {
			return err
		}
		req.SetBasicAuth("magnet-to-strm", password)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("rclone %s 返回 HTTP %d", endpoint, resp.StatusCode)
		}
		if result != nil {
			return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result)
		}
		return nil
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := call("rc/noop", map[string]any{}, nil); err == nil {
			break
		}
		select {
		case <-startup.Done():
			return nil, fmt.Errorf("等待 rclone rcd 启动: %w", startup.Err())
		case <-process.done:
			return nil, fmt.Errorf("rclone rcd 启动失败: %v", process.err)
		case <-ticker.C:
		}
	}
	var server struct {
		Addr string `json:"addr"`
	}
	params := settings.ServeOptions()
	params["type"], params["fs"], params["addr"], params["read_only"] = "http", "mts:", "127.0.0.1:0", true
	for _, line := range settings.Custom {
		name, value, _ := config.ParseRcloneOption(line)
		if config.RcloneServeOption(name) {
			params[strings.ReplaceAll(name, "-", "_")] = value
		}
	}
	err = call("serve/start", params, &server)
	if err != nil {
		return nil, fmt.Errorf("启动 rclone 播放服务: %w", err)
	}
	if server.Addr == "" {
		return nil, errors.New("rclone 未返回播放服务地址")
	}
	process.URL, err = url.Parse("http://" + server.Addr)
	if err != nil {
		return nil, err
	}
	success = true
	return process, nil
}
