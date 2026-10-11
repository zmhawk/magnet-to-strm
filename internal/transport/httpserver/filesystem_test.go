package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathBrowserBoundariesAndSelections(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "allowed")
	outside := filepath.Join(base, "allowed-other")
	for _, dir := range []string{root, outside, filepath.Join(root, "strms"), filepath.Join(root, ".hidden")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".hidden.db"), []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, "existing.sqlite3")
	if err := os.WriteFile(database, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "private.txt"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "missing"), filepath.Join(root, "broken")); err != nil {
		t.Fatal(err)
	}
	browser := &pathBrowser{cwd: root, roots: []browseRoot{{root}}}
	rootsResponse := httptest.NewRecorder()
	browser.ServeHTTP(rootsResponse, httptest.NewRequest(http.MethodGet, "/api/v1/filesystem?kind=directory", nil))
	var rootsBody struct {
		Nodes []pathNode `json:"nodes"`
	}
	if err := json.Unmarshal(rootsResponse.Body.Bytes(), &rootsBody); err != nil {
		t.Fatal(err)
	}
	if len(rootsBody.Nodes) != 1 || rootsBody.Nodes[0].Title != root {
		t.Fatal("root should display only its path")
	}
	canonical, _ := filepath.EvalSymlinks(root)
	for _, tc := range []struct {
		path     string
		database bool
		want     string
		bad      bool
	}{
		{root, true, filepath.Join(canonical, "magnet.db"), false},
		{database, true, filepath.Join(canonical, "existing.sqlite3"), false},
		{"new.db", true, filepath.Join(canonical, "new.db"), false},
		{"new-strms", false, filepath.Join(canonical, "new-strms"), false},
		{database, false, "", true},
		{filepath.Join(root, "private.txt"), true, "", true},
		{outside, true, "", true},
		{filepath.Join(root, "..", "allowed-other"), false, "", true},
		{filepath.Join(root, "escape", "future"), false, "", true},
		{filepath.Join(root, "broken", "future"), false, "", true},
	} {
		got, err := browser.selection(tc.path, tc.database)
		if (err != nil) != tc.bad || (!tc.bad && got != tc.want) {
			t.Fatalf("selection %q: %q, %v", tc.path, got, err)
		}
	}
	for _, kind := range []string{"database", "directory"} {
		rec := httptest.NewRecorder()
		browser.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/filesystem?"+url.Values{"kind": {kind}, "path": {root}}.Encode(), nil))
		if rec.Code != 200 {
			t.Fatalf("listing failed: %s", rec.Body.String())
		}
		var body struct {
			Nodes []pathNode `json:"nodes"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, node := range body.Nodes {
			if node.Title == "escape" || node.Title == "broken" || node.Title == "private.txt" || strings.HasPrefix(node.Title, ".") {
				t.Fatal("listed inaccessible or non-database file")
			}
			if node.Title == "existing.sqlite3" {
				found = true
			}
			if kind == "directory" && !node.Directory {
				t.Fatal("directory picker listed a file")
			}
		}
		if (kind == "database") != found {
			t.Fatal("database listing mismatch")
		}
	}
	rec := httptest.NewRecorder()
	browser.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/filesystem?"+url.Values{"kind": {"database"}, "path": {outside}}.Encode(), nil))
	if rec.Code != 403 {
		t.Fatalf("outside directory returned %d", rec.Code)
	}
}

func TestConfigRejectsOutsideStoragePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	handler := WithConfig(http.NotFoundHandler(), path)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/config", nil))
	// Keep the rest of the configuration valid so only the storage boundary fails.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/config", nil))
	var response struct {
		Settings map[string]map[string]any `json:"settings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	response.Settings["database"]["path"] = "/etc/forbidden.db"
	response.Settings["p115"]["work_dir_id"] = "123"
	payload, _ := json.Marshal(map[string]any{"settings": response.Settings, "credentials": map[string]string{"p115_refresh_token": "synthetic-test-token"}})
	before, _ := os.ReadFile(path)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/config", strings.NewReader(string(payload))))
	if rec.Code != 400 {
		t.Fatalf("outside save returned %d", rec.Code)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("outside save changed configuration")
	}
}
