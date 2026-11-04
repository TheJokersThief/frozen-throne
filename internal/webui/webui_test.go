package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testFS() fs.FS {
	return fstest.MapFS{
		"index.html":      {Data: []byte("<html>app</html>")},
		"assets/app-1.js": {Data: []byte("console.log(1)")},
	}
}

func get(h http.Handler, method, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestServesIndexAtRoot(t *testing.T) {
	rec := get(Handler(testFS()), "GET", "/")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "app") {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("index cache-control = %q", cc)
	}
}

func TestServesAssetsWithLongCache(t *testing.T) {
	rec := get(Handler(testFS()), "GET", "/assets/app-1.js")
	if rec.Code != 200 || rec.Body.String() != "console.log(1)" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Fatalf("asset cache-control = %q", cc)
	}
}

func TestSPAFallbackForClientRoutes(t *testing.T) {
	for _, p := range []string{"/repos/acme/web", "/audit", "/orgs/acme"} {
		rec := get(Handler(testFS()), "GET", p)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "app") {
			t.Fatalf("%s: got %d %q", p, rec.Code, rec.Body.String())
		}
	}
}

func TestMissingAssetIs404NotIndex(t *testing.T) {
	rec := get(Handler(testFS()), "GET", "/assets/missing.js")
	if rec.Code != 404 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestAPIPathsNeverFallBack(t *testing.T) {
	for _, p := range []string{"/api/v1/nope", "/api"} {
		if rec := get(Handler(testFS()), "GET", p); rec.Code != 404 {
			t.Fatalf("%s: got %d", p, rec.Code)
		}
	}
}

func TestNonGETRejected(t *testing.T) {
	if rec := get(Handler(testFS()), "POST", "/"); rec.Code != 405 {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestPlaceholderWhenNotBuilt(t *testing.T) {
	rec := get(Handler(fstest.MapFS{}), "GET", "/")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "web UI") {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}
