package qbittorrent

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"magnet-to-strm/internal/ingest"
	"magnet-to-strm/internal/storage/sqlite"
	"magnet-to-strm/internal/strm"
	"magnet-to-strm/internal/transport/aria2"
)

const testInfoHash = "0123456789abcdef0123456789abcdef01234567"

func TestRequestAndResponseLogging(t *testing.T) {
	var logs []string
	handler := NewHandler(nil, "/strms", "radarr", "secret",
		func(format string, values ...any) {
			logs = append(logs, fmt.Sprintf(format, values...))
		})

	response := request(
		t, handler, http.MethodPost, "/api/v2/auth/login",
		strings.NewReader("username=radarr&password=secret"),
		"application/x-www-form-urlencoded",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("login returned %d", response.Code)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "qBittorrent API 请求:") ||
		!strings.Contains(joined, "qBittorrent API 响应:") ||
		!strings.Contains(joined, "status=200") ||
		!strings.Contains(joined, "password=%5BREDACTED%5D") {
		t.Fatalf("unexpected logs: %s", joined)
	}
	if strings.Contains(joined, "password=secret") {
		t.Fatalf("password leaked in logs: %s", joined)
	}
}

func TestRadarrCompatibleWorkflow(t *testing.T) {
	handler, database, cancel := testHandler(t, "", "")
	defer cancel()
	defer database.Close()

	response := request(t, handler, http.MethodGet, "/api/v2/app/webapiVersion", nil, "")
	if got := response.Body.String(); got != apiVersion {
		t.Fatalf("webapiVersion = %q, want %q", got, apiVersion)
	}

	response = request(
		t, handler, http.MethodPost, "/api/v2/torrents/createCategory",
		strings.NewReader("category=radarr"), "application/x-www-form-urlencoded",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("createCategory returned %d: %s", response.Code, response.Body.String())
	}

	magnet := "magnet:?xt=urn:btih:" + testInfoHash + "&dn=Movie"
	form := url.Values{"urls": {magnet}, "category": {"radarr"}}
	response = request(
		t, handler, http.MethodPost, "/api/v2/torrents/add",
		strings.NewReader(form.Encode()), "application/x-www-form-urlencoded",
	)
	if response.Code != http.StatusOK || response.Body.String() != "Ok." {
		t.Fatalf("add returned %d: %s", response.Code, response.Body.String())
	}
	waitForJob(t, database, testInfoHash[:16])

	response = request(
		t, handler, http.MethodGet, "/api/v2/torrents/info?category=radarr", nil, "",
	)
	var torrents []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &torrents); err != nil {
		t.Fatal(err)
	}
	if len(torrents) != 1 || torrents[0]["state"] != "pausedUP" ||
		torrents[0]["category"] != "radarr" {
		t.Fatalf("unexpected torrents response: %#v", torrents)
	}
	contentPath, _ := torrents[0]["content_path"].(string)
	wantContentPath := filepath.Join(handler.SavePath, "radarr", "Movie")
	if contentPath != wantContentPath {
		t.Fatalf("content_path = %q, want %q", contentPath, wantContentPath)
	}
	if stat, err := os.Stat(contentPath); err != nil || !stat.IsDir() {
		t.Fatalf("content_path %q is not a directory: %v", contentPath, err)
	}

	response = request(
		t, handler, http.MethodGet,
		"/api/v2/torrents/files?hash="+testInfoHash, nil, "",
	)
	var files []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &files); err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0]["name"] != "video.mkv.strm" {
		t.Fatalf("unexpected files response: %#v", files)
	}

	response = request(
		t, handler, http.MethodPost, "/api/v2/torrents/delete",
		strings.NewReader("hashes="+testInfoHash), "application/x-www-form-urlencoded",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("delete returned %d: %s", response.Code, response.Body.String())
	}
	if _, err := database.Job(context.Background(), testInfoHash[:16]); err == nil {
		t.Fatal("deleted job still exists")
	}
}

func TestTorrentInfoReturnsFailedJobAsQBitTorrentError(t *testing.T) {
	handler, database, cancel := testHandler(t, "", "")
	defer cancel()
	defer database.Close()

	job, err := ingest.NewJob("magnet:?xt=urn:btih:" + testInfoHash + "&dn=TimedOut")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UTC()
	job.State = ingest.JobFailed
	job.Error = "任务处理超时：已超过 ingest.job_timeout 配置的最长时限"
	job.FinishedAt = &finished
	if err := database.UpdateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}

	response := request(
		t, handler, http.MethodGet, "/api/v2/torrents/info?filter=errored", nil, "",
	)
	var torrents []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &torrents); err != nil {
		t.Fatal(err)
	}
	if len(torrents) != 1 {
		t.Fatalf("errored torrents = %#v, want one failed task", torrents)
	}
	if torrents[0]["hash"] != testInfoHash || torrents[0]["state"] != "error" {
		t.Fatalf("unexpected failed torrent response: %#v", torrents[0])
	}
}

func TestAuthentication(t *testing.T) {
	handler, database, cancel := testHandler(t, "radarr", "secret")
	defer cancel()
	defer database.Close()

	response := request(t, handler, http.MethodGet, "/api/v2/app/version", nil, "")
	if response.Code != http.StatusForbidden {
		t.Fatalf("anonymous request returned %d, want 403", response.Code)
	}
	response = request(
		t, handler, http.MethodPost, "/api/v2/auth/login",
		strings.NewReader("username=radarr&password=secret"),
		"application/x-www-form-urlencoded",
	)
	if response.Body.String() != "Ok." || len(response.Result().Cookies()) != 1 {
		t.Fatalf("login failed: %d %q", response.Code, response.Body.String())
	}
	cookie := response.Result().Cookies()[0]
	authorized := httptest.NewRequest(http.MethodGet, "/api/v2/app/version", nil)
	authorized.AddCookie(cookie)
	writer := httptest.NewRecorder()
	handler.ServeHTTP(writer, authorized)
	if writer.Code != http.StatusOK || writer.Body.String() != clientVersion {
		t.Fatalf("authenticated request failed: %d %q", writer.Code, writer.Body.String())
	}
}

func TestTorrentUploadCreatesMagnet(t *testing.T) {
	info := []byte("d6:lengthi100e4:name9:video.mkve")
	torrent := append([]byte("d8:announce15:https://tracker4:info"), info...)
	torrent = append(torrent, 'e')
	sum := sha1.Sum(info)
	wantHash := hex.EncodeToString(sum[:])

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("torrents", "movie.torrent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(torrent); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}

	magnet, err := magnetFromTorrent(torrent)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(magnet)
	if parsed.Query().Get("xt") != "urn:btih:"+wantHash ||
		parsed.Query().Get("dn") != "video.mkv" {
		t.Fatalf("unexpected magnet: %s", magnet)
	}

	handler, database, cancel := testHandler(t, "", "")
	defer cancel()
	defer database.Close()
	response := request(
		t, handler, http.MethodPost, "/api/v2/torrents/add",
		&body, form.FormDataContentType(),
	)
	if response.Code != http.StatusOK || response.Body.String() != "Ok." {
		t.Fatalf("torrent upload returned %d: %s", response.Code, response.Body.String())
	}
	if _, err := database.Job(context.Background(), wantHash[:16]); err != nil {
		t.Fatalf("uploaded torrent was not queued: %v", err)
	}
}

func testHandler(
	t *testing.T,
	username string,
	password string,
) (*Handler, *sqlite.DB, context.CancelFunc) {
	t.Helper()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	strmStore, err := strm.New(filepath.Join(t.TempDir(), "strms"), "https://media.test")
	if err != nil {
		t.Fatal(err)
	}
	service := &ingest.Service{
		Provider: qbitProvider{}, Repository: database, WorkDirID: "work",
		STRMStore: strmStore, PollInterval: time.Millisecond,
		PollMinInterval: time.Millisecond, PollMaxInterval: time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager, err := aria2.NewManager(ctx, service, database, 10*time.Second, nil)
	if err != nil {
		cancel()
		database.Close()
		t.Fatal(err)
	}
	return NewHandler(manager, strmStore.RootDir, username, password), database, cancel
}

func request(
	t *testing.T,
	handler http.Handler,
	method string,
	target string,
	body io.Reader,
	contentType string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func waitForJob(t *testing.T, database *sqlite.DB, gid string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		job, err := database.Job(context.Background(), gid)
		if err != nil {
			t.Fatal(err)
		}
		if job.State == ingest.JobSucceeded {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not finish: %+v", job)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type qbitProvider struct{}

func (qbitProvider) ListOfflineTasks(context.Context, int64) ([]ingest.Task, int, error) {
	return []ingest.Task{{
		InfoHash: testInfoHash, Name: "Movie", ResultID: "remote", Status: 2, Done: true,
	}}, 1, nil
}

func (qbitProvider) AddOfflineTask(context.Context, string, string) error { return nil }

func (qbitProvider) AddOfflineTasks(
	context.Context, []string, string,
) ([]ingest.OfflineTaskCreateResult, error) {
	return []ingest.OfflineTaskCreateResult{{InfoHash: testInfoHash, Created: true}}, nil
}

func (qbitProvider) DeleteOfflineTask(context.Context, string, bool) error { return nil }

func (qbitProvider) OfflineFolderInfo(context.Context, string) (ingest.RemoteNode, error) {
	return ingest.RemoteNode{
		ID: "remote", ParentID: "work", Name: "video.mkv",
		SHA1:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SizeBytes: 100, PickCode: "pick", IsDir: false,
		Parents: []ingest.RemoteParent{{ID: "work", Name: "work"}},
	}, nil
}

func (qbitProvider) OfflineListFolder(
	context.Context,
	string,
	int64,
	int64,
) ([]ingest.RemoteNode, int64, error) {
	return nil, 0, nil
}
