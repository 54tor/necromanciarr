package prowlarr

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
)

const fakeKey = "secret-prowlarr-key"

func serve(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(server.URL+"/prefix/?apikey="+fakeKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func writeJSON(writer http.ResponseWriter, body string) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = io.WriteString(writer, body)
}

func TestIndexers_listsEnabledAndDisabledWithTheKeyInTheHeaderOnly(t *testing.T) {
	var gotPath, gotHeader, gotQuery string
	client := serve(t, func(writer http.ResponseWriter, request *http.Request) {
		gotPath, gotHeader, gotQuery = request.URL.Path, request.Header.Get("X-Api-Key"), request.URL.RawQuery
		writeJSON(writer, `[{"id":12,"name":"Tracker A\u0007","enable":true,"capabilities":{"categories":[]}},{"id":7,"name":"B","enable":false}]`)
	})

	indexers, err := client.Indexers(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if gotPath != "/prefix/api/v1/indexer" || gotHeader != fakeKey || gotQuery != "" {
		t.Fatalf("gotValue path %q header %q query %q, expectedValue the prefixed path, the key in the header, no query", gotPath, gotHeader, gotQuery)
	}
	expectedValue := "[{12 Tracker A true} {7 B false}]"
	if fmt.Sprint(indexers) != expectedValue {
		t.Fatalf("gotValue %v, expectedValue %v", indexers, expectedValue)
	}
}

func TestIndexers_mapsStatusesToTypedErrors(t *testing.T) {
	scenarios := map[int]error{
		http.StatusUnauthorized:        ErrAccessDenied,
		http.StatusForbidden:           ErrAccessDenied,
		http.StatusServiceUnavailable:  ErrUnavailable,
		http.StatusTooManyRequests:     ErrUnavailable,
		http.StatusNotFound:            ErrBadResponse,
		http.StatusFound:               ErrBadResponse,
		http.StatusMovedPermanently:    ErrBadResponse,
		http.StatusInternalServerError: ErrUnavailable,
	}
	for status, expectedError := range scenarios {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			redirectTargetHit := false
			client := serve(t, func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/elsewhere" {
					redirectTargetHit = true
				}
				writer.Header().Set("Location", "/elsewhere")
				writer.WriteHeader(status)
				_, _ = io.WriteString(writer, "echo "+request.Header.Get("X-Api-Key"))
			})

			_, gotErr := client.Indexers(context.Background())

			if !errors.Is(gotErr, expectedError) || redirectTargetHit || strings.Contains(gotErr.Error(), fakeKey) {
				t.Fatalf("gotValue %v (redirect followed %v), expectedValue %v without the key", gotErr, redirectTargetHit, expectedError)
			}
		})
	}
}

func TestIndexers_refusesHostileBodies(t *testing.T) {
	scenarios := map[string]http.HandlerFunc{
		"not json content type": func(writer http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(writer, `[]`) },
		"not a list":            func(writer http.ResponseWriter, _ *http.Request) { writeJSON(writer, `{"id":1}`) },
		"truncated":             func(writer http.ResponseWriter, _ *http.Request) { writeJSON(writer, `[{"id":1,"name"`) },
		"too many indexers": func(writer http.ResponseWriter, _ *http.Request) {
			writeJSON(writer, "["+strings.Repeat(`{"id":1},`, maxIndexers)+`{"id":1}]`)
		},
		"body over the cap": func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, "[")
			chunk := strings.Repeat(" ", 1<<20)
			for range maxBodyBytes>>20 + 1 {
				_, _ = io.WriteString(writer, chunk)
			}
		},
	}
	for name, handler := range scenarios {
		t.Run(name, func(t *testing.T) {
			client := serve(t, handler)

			_, gotErr := client.Indexers(context.Background())

			if !errors.Is(gotErr, ErrBadResponse) {
				t.Fatalf("gotValue %v, expectedValue ErrBadResponse", gotErr)
			}
		})
	}
}

func TestIndexers_transportErrorsNeverContainTheKey(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	client, err := New(server.URL+"?apikey="+fakeKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.Close()

	_, gotErr := client.Indexers(context.Background())

	if !errors.Is(gotErr, ErrUnavailable) || strings.Contains(gotErr.Error(), fakeKey) || strings.Contains(gotErr.Error(), server.URL) {
		t.Fatalf("gotValue %v, expectedValue ErrUnavailable without the key nor the URL", gotErr)
	}
}

func TestIndexers_canceledContextIsUnavailable(t *testing.T) {
	client := serve(t, func(writer http.ResponseWriter, request *http.Request) { <-request.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, gotErr := client.Indexers(ctx)

	if !errors.Is(gotErr, ErrUnavailable) || !errors.Is(gotErr, context.DeadlineExceeded) {
		t.Fatalf("gotValue %v, expectedValue ErrUnavailable wrapping the deadline", gotErr)
	}
}

func TestNew_refusesInvalidURLsWithoutEchoingThem(t *testing.T) {
	for name, rawURL := range map[string]string{
		"extra parameter": "https://example.com:9696?apikey=secret-k&x=1",
		"userinfo":        "https://bob:secret-p@example.com:9696?apikey=secret-k",
		"fragment":        "https://example.com?apikey=secret-k#a",
		"no key":          "https://example.com",
		"ftp":             "ftp://example.com?apikey=secret-k",
		"key twice":       "https://example.com?apikey=secret-k&apikey=secret-j",
	} {
		t.Run(name, func(t *testing.T) {
			_, gotErr := New(rawURL, nil)

			if gotErr == nil || strings.Contains(gotErr.Error(), "secret-") || strings.Contains(gotErr.Error(), "example.com") {
				t.Fatalf("gotValue %v, expectedValue an error without the URL", gotErr)
			}
		})
	}
}

func TestClient_textFormsNeverShowTheKey(t *testing.T) {
	client, err := New("https://example.com:9696/p?apikey="+fakeKey, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, rendering := range []string{fmt.Sprintf("%v", client), fmt.Sprintf("%+v", client), fmt.Sprintf("%#v", client), client.String(), client.LogValue().String()} {
		if strings.Contains(rendering, fakeKey) || !strings.Contains(rendering, "example.com:9696") {
			t.Fatalf("gotValue %q, expectedValue the host without the key", rendering)
		}
	}
}
