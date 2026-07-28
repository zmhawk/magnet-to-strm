package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRedirectRouteReplacesProxyRoute(t *testing.T) {
	handler := NewHandler(
		nil, nil, nil, nil,
		http.NotFoundHandler(), http.NotFoundHandler(), false, nil,
	)

	redirectResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		redirectResponse,
		httptest.NewRequest(http.MethodGet, "/redirect/not-a-sha1", nil),
	)
	if redirectResponse.Code != http.StatusBadRequest {
		t.Fatalf("redirect route returned %d, want %d", redirectResponse.Code, http.StatusBadRequest)
	}

	proxyResponse := httptest.NewRecorder()
	handler.ServeHTTP(
		proxyResponse,
		httptest.NewRequest(
			http.MethodGet,
			"/proxy/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			nil,
		),
	)
	if proxyResponse.Code != http.StatusNotFound {
		t.Fatalf("proxy route returned %d, want %d", proxyResponse.Code, http.StatusNotFound)
	}
}

func TestRedirectRouteRejectsInvalidInfoHash(t *testing.T) {
	handler := NewHandler(
		nil, nil, nil, nil,
		http.NotFoundHandler(), http.NotFoundHandler(), false, nil,
	)
	for _, target := range []string{
		"/redirect/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa?info_hash=bad",
		"/redirect/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" +
			"?info_hash=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" +
			"&info_hash=cccccccccccccccccccccccccccccccccccccccc",
		"/redirect/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa?source=unknown",
		"/redirect/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa?info_hash=bad;value",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(
			response, httptest.NewRequest(http.MethodGet, target, nil),
		)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s returned %d, want %d",
				target, response.Code, http.StatusBadRequest)
		}
	}
}

func TestRootServesWebUI(t *testing.T) {
	handler := NewHandler(
		nil, nil, nil, nil,
		http.NotFoundHandler(), http.NotFoundHandler(), false, nil,
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("root returned %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(response.Body.String(), "<title>Magnet to STRM</title>") {
		t.Fatalf("root does not serve WebUI: %q", response.Body.String())
	}
}
