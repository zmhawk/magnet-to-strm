package httpserver

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type browseRoot struct{ Path string }
type pathBrowser struct {
	roots []browseRoot
	cwd   string
}
type pathNode struct {
	Key       string `json:"key"`
	Title     string `json:"title"`
	Path      string `json:"path"`
	Directory bool   `json:"is_directory"`
	Leaf      bool   `json:"isLeaf"`
	Disabled  bool   `json:"disabled,omitempty"`
}

func newPathBrowser() (*pathBrowser, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &pathBrowser{cwd: cwd, roots: []browseRoot{
		{"/data"}, {home}, {cwd},
	}}, nil
}

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Resolve the nearest existing ancestor as well as the requested path. This
// prevents symlinks and nonexistent descendants from escaping the allowed roots.
func canonicalPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if _, linkErr := os.Lstat(path); linkErr == nil {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err = canonicalPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(path)), nil
}

func (b *pathBrowser) resolve(path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(b.cwd, path)
	}
	path = filepath.Clean(path)
	resolved, err := canonicalPath(path)
	if err != nil {
		return "", err
	}
	for _, root := range b.roots {
		canonical, err := filepath.EvalSymlinks(root.Path)
		if err == nil && (withinRoot(root.Path, path) || withinRoot(canonical, path)) && withinRoot(canonical, resolved) {
			return resolved, nil
		}
	}
	return "", errors.New("路径必须位于 /data、当前用户目录或程序工作目录内")
}

func databaseFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".db", ".sqlite", ".sqlite3":
		return true
	default:
		return false
	}
}

func (b *pathBrowser) selection(path string, database bool) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("请选择路径")
	}
	resolved, err := b.resolve(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if database {
		if info != nil && info.IsDir() {
			resolved, err = b.resolve(filepath.Join(resolved, "magnet.db"))
			if err != nil {
				return "", err
			}
			info, err = os.Stat(resolved)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		}
		if !databaseFile(resolved) || (info != nil && !info.Mode().IsRegular()) {
			return "", errors.New("请选择目录或 .db、.sqlite、.sqlite3 数据库文件")
		}
	} else if info != nil && !info.IsDir() {
		return "", errors.New("STRM 输出路径必须是目录")
	}
	return resolved, nil
}

func (b *pathBrowser) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "database" && kind != "directory" {
		writeAPIError(w, 400, "无效的路径选择类型")
		return
	}
	path := r.URL.Query().Get("path")
	nodes := make([]pathNode, 0)
	if path == "" {
		seen := map[string]bool{}
		for _, root := range b.roots {
			if seen[root.Path] {
				continue
			}
			seen[root.Path] = true
			info, err := os.Stat(root.Path)
			disabled := err != nil || !info.IsDir()
			nodes = append(nodes, pathNode{Key: root.Path, Path: root.Path, Title: root.Path, Directory: true, Disabled: disabled, Leaf: disabled})
		}
	} else {
		resolved, err := b.resolve(path)
		if err != nil {
			writeAPIError(w, 403, "路径不在允许浏览的范围内")
			return
		}
		entries, err := os.ReadDir(resolved)
		if err != nil {
			writeAPIError(w, 400, "无法读取目录，请检查路径和访问权限")
			return
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			child := filepath.Join(resolved, entry.Name())
			safe, err := b.resolve(child)
			if err != nil {
				continue
			}
			info, err := os.Stat(safe)
			if err != nil {
				continue
			}
			if !info.IsDir() && (kind != "database" || !info.Mode().IsRegular() || !databaseFile(safe)) {
				continue
			}
			// Keep lexical paths as tree keys: aliases to the same target remain distinct.
			nodes = append(nodes, pathNode{Key: child, Path: child, Title: entry.Name(), Directory: info.IsDir(), Leaf: !info.IsDir()})
		}
		sort.SliceStable(nodes, func(i, j int) bool {
			if nodes[i].Directory != nodes[j].Directory {
				return nodes[i].Directory
			}
			return strings.ToLower(nodes[i].Title) < strings.ToLower(nodes[j].Title)
		})
	}
	writeJSON(w, 200, map[string]any{"nodes": nodes})
}
