package webdav

import (
	"context"
	"errors"
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

func TestGetUsesStoredPathAndStreamsRange(t *testing.T) {
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
}

func TestGetUsesExplicitUpstreamBasicAuth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		username, password, ok := request.BasicAuth()
		if !ok || username != "dav-user" || password != "dav-password" {
			t.Fatalf("unexpected upstream credentials: %q %q %v", username, password, ok)
		}
		_, _ = io.WriteString(writer, "content")
	}))
	defer upstream.Close()

	handler := testHandler(t, upstream.URL+"/current/video.mkv")
	handler.UpstreamUsername = "dav-user"
	handler.UpstreamPassword = "dav-password"
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
		Resolver:   fakeResolver{target: target},
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
	target string
}

func (r fakeResolver) Redirect(context.Context, string) (string, error) {
	if r.target == "" {
		return "", errors.New("target is not configured")
	}
	return r.target, nil
}
