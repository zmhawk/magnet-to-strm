package strm

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const maxSTRMSize = 16 * 1024

type Service struct {
	RootDir       string
	PublicBaseURL string
}

type ReplaceResult struct {
	Scanned   int
	Rewritten int
}

func New(rootDir, publicBaseURL string) (*Service, error) {
	absoluteRoot, err := filepath.Abs(strings.TrimSpace(rootDir))
	if err != nil {
		return nil, fmt.Errorf("解析 STRM 目录: %w", err)
	}
	if strings.TrimSpace(rootDir) == "" {
		return nil, errors.New("STRM 目录不能为空")
	}
	base := strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("public_base_url 必须是有效的 HTTP 或 HTTPS 地址")
	}
	return &Service{RootDir: absoluteRoot, PublicBaseURL: base}, nil
}

func (s *Service) Path(relativePath string) (string, error) {
	clean, err := cleanRelativePath(relativePath)
	if err != nil {
		return "", err
	}
	target := filepath.Join(s.RootDir, clean)
	relative, err := filepath.Rel(s.RootDir, target)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("STRM 路径越过配置的输出目录")
	}
	return target, nil
}

func (s *Service) Write(
	ctx context.Context,
	relativePath string,
	sha1Value string,
	infoHash string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	sha1Value = strings.ToLower(strings.TrimSpace(sha1Value))
	if !validSHA1(sha1Value) {
		return "", fmt.Errorf("无效 SHA1 %q", sha1Value)
	}
	infoHash = strings.ToLower(strings.TrimSpace(infoHash))
	if infoHash != "" && !validSHA1(infoHash) {
		return "", fmt.Errorf("无效 info hash %q", infoHash)
	}
	target, err := s.Path(relativePath)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Lstat(target); statErr == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("STRM 目标不是普通文件: %s", target)
		}
		content, readErr := readSTRM(target)
		if readErr != nil {
			return "", readErr
		}
		_, managed := redirectSHA1(content)
		if !managed {
			return "", fmt.Errorf("拒绝覆盖非本程序管理的 STRM: %s", target)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	if err := atomicWrite(
		s.RootDir, target, []byte(s.content(sha1Value, infoHash)),
	); err != nil {
		return "", err
	}
	return target, nil
}

func (s *Service) ReplacePrefix(
	ctx context.Context,
	oldPrefix string,
	newPrefix string,
) (ReplaceResult, error) {
	var result ReplaceResult
	oldPrefix, err := normalizePrefix(oldPrefix)
	if err != nil {
		return result, fmt.Errorf("无效旧前缀: %w", err)
	}
	newPrefix, err = normalizePrefix(newPrefix)
	if err != nil {
		return result, fmt.Errorf("无效新前缀: %w", err)
	}
	if oldPrefix == newPrefix {
		return result, errors.New("新旧前缀不能相同")
	}
	info, err := os.Stat(s.RootDir)
	if err != nil {
		return result, fmt.Errorf("读取 STRM 目录: %w", err)
	}
	if !info.IsDir() {
		return result, fmt.Errorf("STRM 路径不是目录: %s", s.RootDir)
	}
	err = filepath.WalkDir(s.RootDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !entry.Type().IsRegular() ||
			!strings.EqualFold(filepath.Ext(entry.Name()), ".strm") {
			return nil
		}
		result.Scanned++
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxSTRMSize {
			return nil
		}
		content, err := readSTRM(path)
		if err != nil {
			return err
		}
		value := strings.TrimSpace(content)
		if strings.ContainsAny(value, "\r\n\t ") ||
			!strings.HasPrefix(value, oldPrefix) {
			return nil
		}
		suffix := strings.TrimPrefix(value, oldPrefix)
		if suffix == "" || !strings.HasPrefix(suffix, "/") {
			return nil
		}
		replacement := newPrefix + suffix + "\n"
		if err := atomicWrite(s.RootDir, path, []byte(replacement)); err != nil {
			return err
		}
		result.Rewritten++
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("替换 STRM 前缀: %w", err)
	}
	return result, nil
}

func (s *Service) content(sha1Value string, infoHash string) string {
	value := s.PublicBaseURL + "/redirect/" + strings.ToLower(sha1Value)
	if infoHash != "" {
		value += "?info_hash=" + url.QueryEscape(strings.ToLower(infoHash))
	}
	return value + "\n"
}

func cleanRelativePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) || strings.ContainsRune(value, 0) {
		return "", errors.New("无效 STRM 相对路径")
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	if clean == "." || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("无效 STRM 相对路径")
	}
	return clean, nil
}

func readSTRM(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > maxSTRMSize {
		return "", fmt.Errorf("STRM 文件过大: %s", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func redirectSHA1(content string) (string, bool) {
	value := strings.TrimSpace(content)
	if value == "" || strings.ContainsAny(value, "\r\n\t ") {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Fragment != "" {
		return "", false
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", false
	}
	for key := range query {
		if key != "info_hash" {
			return "", false
		}
	}
	if values, ok := query["info_hash"]; ok {
		if len(values) != 1 || !validSHA1(strings.ToLower(values[0])) {
			return "", false
		}
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(segments) < 2 || segments[len(segments)-2] != "redirect" {
		return "", false
	}
	sha1Value, err := url.PathUnescape(segments[len(segments)-1])
	if err != nil {
		return "", false
	}
	sha1Value = strings.ToLower(sha1Value)
	return sha1Value, validSHA1(sha1Value)
}

func normalizePrefix(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" || strings.ContainsAny(value, "\r\n\t ") {
		return "", errors.New("URL 前缀不能为空或包含空白字符")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("URL 前缀必须是有效的 HTTP 或 HTTPS 地址")
	}
	return value, nil
}

func validSHA1(value string) bool {
	if len(value) != 40 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func atomicWrite(root, target string, content []byte) error {
	dir := filepath.Dir(target)
	if err := ensureDirectories(root, dir); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".strm-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, target)
}

func ensureDirectories(root, dir string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	relative, err := filepath.Rel(root, dir)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("STRM 目录越过配置的输出目录")
	}
	current := root
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		if segment == "" || segment == "." {
			continue
		}
		current = filepath.Join(current, segment)
		err := os.Mkdir(current, 0o755)
		if err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("STRM 输出路径包含非目录或符号链接: %s", current)
		}
	}
	return nil
}
