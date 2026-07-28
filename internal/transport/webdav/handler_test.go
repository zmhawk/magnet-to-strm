package webdav

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"magnet-to-strm/internal/materialize"
)

const testSHA1 = "aabb000000000000000000000000000000000000"

func TestPropfindReadsStableObjectProperties(t *testing.T) {
	handler := testHandler(t, "")
	request := httptest.NewRequest(
		"PROPFIND",
		"/dav/objects/aa/bb/"+testSHA1+".mkv",
		strings.NewReader(`<propfind xmlns="DAV:"><allprop/></propfind>`),
	)
	request.Header.Set("Depth", "0")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND returned %d: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{
		"/dav/objects/aa/bb/" + testSHA1 + ".mkv",
		">1234</getcontentlength>",
		">&#34;" + testSHA1 + "&#34;</getetag>",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("PROPFIND response does not contain %q: %s", expected, body)
		}
	}
}

func TestPropfindListsHashBucketsFromRepository(t *testing.T) {
	handler := testHandler(t, "")
	request := httptest.NewRequest("PROPFIND", "/dav/objects/aa/", nil)
	request.Header.Set("Depth", "1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND returned %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "/dav/objects/aa/bb/") {
		t.Fatalf("second-level bucket is missing: %s", response.Body.String())
	}
}

func TestGetUses115DownloadURLAndStreamsRange(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/current/video.mkv" {
			t.Fatalf("upstream path = %q", request.URL.Path)
		}
		if request.Header.Get("Range") != "bytes=10-19" {
			t.Fatalf("Range was not forwarded: %q", request.Header.Get("Range"))
		}
		if request.Header.Get("Authorization") != "" {
			t.Fatal("client Authorization leaked to upstream")
		}
		if request.UserAgent() != "test-player/1.0" {
			t.Fatalf("unexpected upstream User-Agent: %q", request.UserAgent())
		}
		writer.Header().Set("Content-Range", "bytes 10-19/1234")
		writer.Header().Set("ETag", `"unstable-upstream-etag"`)
		writer.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(writer, "0123456789")
	}))
	defer upstream.Close()

	handler := testHandler(t, upstream.URL+"/current/video.mkv")
	request := httptest.NewRequest(
		http.MethodGet,
		"/dav/objects/aa/bb/"+testSHA1+".mkv",
		nil,
	)
	request.Header.Set("Range", "bytes=10-19")
	request.Header.Set("Authorization", "Bearer local-secret")
	request.Header.Set("User-Agent", "test-player/1.0")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusPartialContent || response.Body.String() != "0123456789" {
		t.Fatalf("GET returned %d %q", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Range") != "bytes 10-19/1234" {
		t.Fatalf("Content-Range was not copied: %q", response.Header().Get("Content-Range"))
	}
	if response.Header().Get("ETag") != `"`+testSHA1+`"` {
		t.Fatalf("ETag is not stable: %q", response.Header().Get("ETag"))
	}
	downloader := handler.Downloader.(*fakeDownloader)
	if len(downloader.userAgents) != 1 || downloader.userAgents[0] != "test-player/1.0" {
		t.Fatalf("download URL used unexpected User-Agent: %#v", downloader.userAgents)
	}
}

func TestDownloadURLIsCachedByPickCodeAndUserAgent(t *testing.T) {
	var upstreamRequests int
	upstream := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		upstreamRequests++
		_, _ = io.WriteString(writer, "content")
	}))
	defer upstream.Close()

	handler := testHandler(t, upstream.URL+"/current/video.mkv")
	for _, userAgent := range []string{"player-a", "player-a", "player-b"} {
		request := httptest.NewRequest(
			http.MethodGet,
			"/dav/objects/aa/bb/"+testSHA1+".mkv",
			nil,
		)
		request.Header.Set("User-Agent", userAgent)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("GET for %s returned %d", userAgent, response.Code)
		}
	}

	downloader := handler.Downloader.(*fakeDownloader)
	if upstreamRequests != 3 {
		t.Fatalf("upstream requests = %d, want 3", upstreamRequests)
	}
	if got := downloader.userAgents; len(got) != 2 ||
		got[0] != "player-a" || got[1] != "player-b" {
		t.Fatalf("download URL calls = %#v, want one per User-Agent", got)
	}
}

func TestExpiredDownloadURLIsRefreshedOnce(t *testing.T) {
	var requests int
	upstream := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		requests++
		if requests == 1 {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(writer, "content")
	}))
	defer upstream.Close()

	handler := testHandler(t, upstream.URL+"/current/video.mkv")
	var logs []string
	handler.Logf = func(format string, values ...any) {
		logs = append(logs, fmt.Sprintf(format, values...))
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/dav/objects/aa/bb/"+testSHA1+".mkv",
		nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Body.String() != "content" {
		t.Fatalf("GET returned %d %q", response.Code, response.Body.String())
	}
	downloader := handler.Downloader.(*fakeDownloader)
	if requests != 2 || len(downloader.userAgents) != 2 {
		t.Fatalf("download URL was not refreshed once: requests=%d calls=%d",
			requests, len(downloader.userAgents))
	}
	if len(logs) != 1 ||
		!strings.Contains(logs[0], "pick_code=pick-code") ||
		!strings.Contains(logs[0], "status=403") ||
		!strings.Contains(logs[0], "cached_at=") ||
		!strings.Contains(logs[0], "age=") {
		t.Fatalf("missing download URL expiry details: %#v", logs)
	}
}

func TestConditionalGetUsesStableETagWithoutUpstreamRequest(t *testing.T) {
	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(
		http.ResponseWriter,
		*http.Request,
	) {
		called = true
	}))
	defer upstream.Close()

	handler := testHandler(t, upstream.URL+"/current/video.mkv")
	request := httptest.NewRequest(
		http.MethodGet,
		"/dav/objects/aa/bb/"+testSHA1+".mkv",
		nil,
	)
	request.Header.Set("If-None-Match", `W/"`+testSHA1+`"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNotModified {
		t.Fatalf("conditional GET returned %d, want %d", response.Code, http.StatusNotModified)
	}
	if called {
		t.Fatal("conditional GET reached upstream")
	}
	if len(handler.Downloader.(*fakeDownloader).userAgents) != 0 {
		t.Fatal("conditional GET requested a 115 download URL")
	}
}

func TestMutatingMethodsAreRejected(t *testing.T) {
	handler := testHandler(t, "")
	response := httptest.NewRecorder()
	handler.ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodPut,
			"/dav/objects/aa/bb/"+testSHA1+".mkv",
			strings.NewReader("changed"),
		),
	)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT returned %d, want %d", response.Code, http.StatusMethodNotAllowed)
	}
}

func testHandler(t *testing.T, target string) *Handler {
	t.Helper()
	asset := materialize.Asset{
		ID: 1, SHA1: testSHA1, SizeBytes: 1234, PreferredName: "video.mkv",
		CreatedAt: time.Date(2026, time.July, 26, 12, 0, 0, 0, time.UTC),
	}
	return &Handler{
		Repository: fakeRepository{assets: []materialize.Asset{asset}},
		Resolver: fakeResolver{resolution: materialize.Resolution{
			Asset: asset,
			Location: materialize.Location{
				PickCode: "pick-code", RemotePath: "/current/video.mkv",
			},
		}},
		Downloader: &fakeDownloader{target: target},
		HTTPClient: &http.Client{},
	}
}

type fakeRepository struct {
	assets []materialize.Asset
}

func (r fakeRepository) AssetBySHA1(
	_ context.Context,
	sha1Value string,
) (materialize.Asset, error) {
	for _, asset := range r.assets {
		if asset.SHA1 == sha1Value {
			return asset, nil
		}
	}
	return materialize.Asset{}, materialize.ErrAssetNotFound
}

func (r fakeRepository) AssetsBySHA1Prefix(
	_ context.Context,
	prefix string,
) ([]materialize.Asset, error) {
	var result []materialize.Asset
	for _, asset := range r.assets {
		if strings.HasPrefix(asset.SHA1, prefix) {
			result = append(result, asset)
		}
	}
	return result, nil
}

func (r fakeRepository) SHA1Prefixes(
	_ context.Context,
	prefix string,
	length int,
) ([]string, error) {
	seen := make(map[string]bool)
	var result []string
	for _, asset := range r.assets {
		if strings.HasPrefix(asset.SHA1, prefix) && !seen[asset.SHA1[:length]] {
			value := asset.SHA1[:length]
			seen[value] = true
			result = append(result, value)
		}
	}
	return result, nil
}

type fakeResolver struct {
	resolution materialize.Resolution
}

func (r fakeResolver) Resolve(
	context.Context,
	string,
) (materialize.Resolution, error) {
	if r.resolution.Asset.SHA1 == "" {
		return materialize.Resolution{}, errors.New("target is not configured")
	}
	return r.resolution, nil
}

type fakeDownloader struct {
	target     string
	userAgents []string
}

func (d *fakeDownloader) DownloadURL(
	_ context.Context,
	_ string,
	userAgent string,
) (string, error) {
	d.userAgents = append(d.userAgents, userAgent)
	if d.target == "" {
		return "", errors.New("target is not configured")
	}
	return d.target, nil
}
