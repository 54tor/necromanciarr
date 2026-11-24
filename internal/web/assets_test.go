package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestServer_fallsBackToIndexForClientRoutes(t *testing.T) {
	handler := newTestHandler(t, &fakeService{}, loopbackSettings())
	for _, target := range []string{"/", "/index.html", "/proposals", "/proposals/12/details", "/assets"} {
		response := serve(handler, apiRequest(http.MethodGet, target, ""))
		if response.Code != http.StatusOK || readBody(t, response) != indexBody {
			t.Fatalf("%s: gotValue %d %q, expectedValue the index", target, response.Code, readBody(t, response))
		}
		if !strings.HasPrefix(response.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s: gotValue %q, expectedValue text/html", target, response.Header().Get("Content-Type"))
		}
	}
	asset := serve(handler, apiRequest(http.MethodGet, "/assets/app.js", ""))
	if asset.Code != http.StatusOK || readBody(t, asset) != assetBody {
		t.Fatalf("gotValue %d %q, expectedValue the asset", asset.Code, readBody(t, asset))
	}
	for _, target := range []string{"/assets/missing.js", "/favicon.ico", "/proposals/report.json"} {
		if gotValue := serve(handler, apiRequest(http.MethodGet, target, "")).Code; gotValue != http.StatusNotFound {
			t.Fatalf("%s: gotValue %d, expectedValue 404 for a missing file", target, gotValue)
		}
	}
	if gotValue := serve(handler, apiRequest(http.MethodPost, "/", "")).Code; gotValue != http.StatusMethodNotAllowed {
		t.Fatalf("gotValue %d, expectedValue 405 for a post on the interface", gotValue)
	}
}

func TestServer_fallbackPageWhenUiNotBuilt(t *testing.T) {
	placeholderOnly := fstest.MapFS{placeholderTag: {Data: []byte("placeholder")}}
	handler := newTestHandlerWith(t, &fakeService{}, &fakeHealth{}, loopbackSettings(), placeholderOnly)

	for _, target := range []string{"/", "/proposals"} {
		response := serve(handler, apiRequest(http.MethodGet, target, ""))

		body := readBody(t, response)
		if response.Code != http.StatusOK || !strings.Contains(body, "Interface non construite") || !strings.Contains(body, "make ui") {
			t.Fatalf("%s: gotValue %d %q, expectedValue the built-in page", target, response.Code, body)
		}
		if strings.Contains(body, "http") || strings.Contains(body, "<script") || strings.Contains(body, "style") {
			t.Fatalf("%s: gotValue %q, expectedValue no external resource, script nor inline style", target, body)
		}
		if response.Header().Get("Content-Security-Policy") != contentSecurityPolicy {
			t.Fatalf("%s: gotValue no CSP, expectedValue the security headers", target)
		}
	}
	if gotValue := serve(handler, apiRequest(http.MethodGet, "/assets/app.js", "")).Code; gotValue != http.StatusNotFound {
		t.Fatalf("gotValue %d, expectedValue 404 for an asset of an unbuilt interface", gotValue)
	}
	if gotValue := serve(handler, apiRequest(http.MethodGet, "/api/status", "")).Code; gotValue != http.StatusOK {
		t.Fatalf("gotValue %d, expectedValue the API still served", gotValue)
	}
}

func TestServer_assetsNoTraversal(t *testing.T) {
	const secret = "outside-of-dist"
	tree := fstest.MapFS{
		"dist/index.html":    {Data: []byte(indexBody)},
		"dist/assets/app.js": {Data: []byte(assetBody)},
		"secret.txt":         {Data: []byte(secret)},
	}
	dist, err := fs.Sub(tree, "dist")
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHandlerWith(t, &fakeService{}, &fakeHealth{}, loopbackSettings(), dist)
	for _, target := range []string{"/../secret.txt", "/assets/../../secret.txt", "/%2e%2e/secret.txt", "/assets/..%2f..%2fsecret.txt", "/..%5csecret.txt"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Host = testHost

		response := serve(handler, request)

		body := readBody(t, response)
		if strings.Contains(body, secret) || (response.Code == http.StatusOK && body != indexBody) {
			t.Fatalf("%s: gotValue %d %q, expectedValue no file outside the assets", target, response.Code, body)
		}
	}
}

func TestServer_nestedClientRouteServesAnIndexWithAbsoluteAssetURLs(t *testing.T) {
	const viteIndex = `<!doctype html><script type="module" src="/assets/index-abc.js"></script>` +
		`<link rel="stylesheet" href="/assets/index-abc.css">`
	assets := fstest.MapFS{"index.html": {Data: []byte(viteIndex)}, "assets/index-abc.js": {Data: []byte(assetBody)}}
	handler := newTestHandlerWith(t, &fakeService{}, &fakeHealth{}, loopbackSettings(), assets)

	response := serve(handler, apiRequest(http.MethodGet, "/proposals/12", ""))

	body := readBody(t, response)
	if response.Code != http.StatusOK || body != viteIndex || strings.Contains(body, `"./assets`) {
		t.Fatalf("gotValue %d %q, expectedValue the index with absolute asset urls", response.Code, body)
	}
	if gotValue := serve(handler, apiRequest(http.MethodGet, "/assets/index-abc.js", "")).Code; gotValue != http.StatusOK {
		t.Fatalf("gotValue %d, expectedValue the asset served at its absolute url", gotValue)
	}
}
