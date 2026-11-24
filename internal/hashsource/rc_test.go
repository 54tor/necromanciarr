package hashsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const rcPassword = "S3CRET-RC-PASS"

var rcMounts = []Mount{{LocalPath: "/home/u/mnt/remote", Remote: "remote:bucket/prefix"}}

type recordedCall struct {
	method, path, authorization, contentType string
	body                                     map[string]any
}

// rcServer enregistre chaque appel et répond avec handler.
type rcServer struct {
	*httptest.Server
	mutex sync.Mutex
	calls []recordedCall
}

func newRCServer(t *testing.T, handler func(http.ResponseWriter, *http.Request)) *rcServer {
	t.Helper()
	server := &rcServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		content, _ := io.ReadAll(request.Body)
		call := recordedCall{method: request.Method, path: request.URL.Path, authorization: request.Header.Get("Authorization"), contentType: request.Header.Get("Content-Type")}
		_ = json.Unmarshal(content, &call.body)
		server.mutex.Lock()
		server.calls = append(server.calls, call)
		server.mutex.Unlock()
		handler(writer, request)
	}))
	t.Cleanup(server.Close)
	return server
}

func respondWith(body string) func(http.ResponseWriter, *http.Request) {
	return func(writer http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(writer, body) }
}

func (server *rcServer) withCredentials() string {
	return strings.Replace(server.URL, "http://", "http://rcuser:"+rcPassword+"@", 1)
}

func newRC(t *testing.T, url string, timeout time.Duration) HashSource {
	t.Helper()
	source, err := NewRcloneRC(RcOptions{URL: url, Mounts: rcMounts, Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestRcloneRC_sendsStatRequestWithBasicAuthAndStrippedUserinfo(t *testing.T) {
	server := newRCServer(t, respondWith(`{"item":{"Name":"a.mkv","IsDir":false,"Hashes":{"md5":"`+strings.ToUpper(md5Value)+`"}}}`))

	result, err := newRC(t, server.withCredentials(), 0).Hash(context.Background(), "/home/u/mnt/remote/films/a.mkv", MD5)

	if err != nil || !result.Available || result.Value != md5Value || result.Origin != Backend {
		t.Fatalf("%+v %v", result, err)
	}
	call := server.calls[0]
	request, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	request.SetBasicAuth("rcuser", rcPassword)
	if call.method != http.MethodPost || call.path != "/operations/stat" || call.authorization != request.Header.Get("Authorization") || call.contentType != "application/json" {
		t.Fatalf("unexpected call %+v", call)
	}
	expectedBody := `{"fs":"remote:bucket/prefix","opt":{"hashTypes":["md5"],"noMimeType":true,"showHash":true},"remote":"films/a.mkv"}`
	gotBody, _ := json.Marshal(call.body)
	if string(gotBody) != expectedBody {
		t.Fatalf("got body %s, expected %s", gotBody, expectedBody)
	}
}

func TestRcloneRC_withoutCredentialsSendsNoAuthorization(t *testing.T) {
	server := newRCServer(t, respondWith(`{"item":null}`))

	result, err := newRC(t, server.URL, 0).Hash(context.Background(), "/home/u/mnt/remote/a.mkv", MD5)

	if err != nil || result.Available || server.calls[0].authorization != "" {
		t.Fatalf("%+v %v %q", result, err, server.calls[0].authorization)
	}
}

func TestRcloneRC_onlyCallsAllowListedMethodAndNeverDownloads(t *testing.T) {
	server := newRCServer(t, respondWith(`{"item":{"Name":"a.mkv","Hashes":{"md5":"`+md5Value+`"}}}`))
	source := newRC(t, server.URL, 0)

	for _, algorithm := range AllAlgorithms {
		if _, err := source.Hash(context.Background(), "/home/u/mnt/remote/a.mkv", algorithm); err != nil && !errors.Is(err, ErrRcloneFailed) {
			t.Fatal(err)
		}
	}

	for _, call := range server.calls {
		if call.path != "/operations/stat" {
			t.Fatalf("method outside the allow-list: %s", call.path)
		}
		if _, hasDownload := call.body["download"]; hasDownload {
			t.Fatalf("download parameter sent: %v", call.body)
		}
	}
	rcSource := source.(*rcloneRC)
	for _, forbidden := range []string{"core/command", "job/list", "sync/copy", "operations/copyfile", "operations/deletefile", "operations/hashsum"} {
		if err := rcSource.call(context.Background(), forbidden, struct{}{}, &rcStatResponse{}); !errors.Is(err, ErrRcloneFailed) {
			t.Fatalf("%s: got %v", forbidden, err)
		}
	}
	if len(server.calls) != len(AllAlgorithms) {
		t.Fatalf("forbidden methods reached the server: %d calls", len(server.calls))
	}
}

func TestRcloneRC_refusesRedirectsAndNeverSendsCredentialsElsewhere(t *testing.T) {
	second := newRCServer(t, respondWith(`{"item":null}`))
	first := newRCServer(t, func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, second.URL+"/operations/stat", http.StatusTemporaryRedirect)
	})

	_, err := newRC(t, first.withCredentials(), 0).Hash(context.Background(), "/home/u/mnt/remote/a.mkv", MD5)

	if !errors.Is(err, ErrRcloneFailed) || strings.Contains(err.Error(), rcPassword) {
		t.Fatalf("got %v", err)
	}
	if len(second.calls) != 0 {
		t.Fatalf("redirect followed: %+v", second.calls)
	}
}

func TestRcloneRC_capsResponseSize(t *testing.T) {
	server := newRCServer(t, respondWith(`{"item":null,"pad":"`+strings.Repeat("a", maxRCResponseBytes)+`"}`))

	_, err := newRC(t, server.URL, 0).Hash(context.Background(), "/home/u/mnt/remote/a.mkv", MD5)

	if !errors.Is(err, ErrRcloneFailed) {
		t.Fatalf("got %v", err)
	}
}

func TestRcloneRC_rejectsHostileResponses(t *testing.T) {
	cases := map[string]string{
		"not json":          `<html>nope</html>`,
		"trailing data":     `{"item":null}{"item":null}`,
		"wrong type":        `{"item":"a.mkv"}`,
		"other item name":   `{"item":{"Name":"other.mkv","Hashes":{"md5":"` + md5Value + `"}}}`,
		"not hex":           `{"item":{"Name":"a.mkv","Hashes":{"md5":"` + strings.Repeat("z", 32) + `"}}}`,
		"wrong hash length": `{"item":{"Name":"a.mkv","Hashes":{"md5":"` + sha1Value + `"}}}`,
		"hashes not a map":  `{"item":{"Name":"a.mkv","Hashes":["md5"]}}`,
		"empty body":        ``,
	}
	for name, body := range cases {
		server := newRCServer(t, respondWith(body))

		result, err := newRC(t, server.URL, 0).Hash(context.Background(), "/home/u/mnt/remote/a.mkv", MD5)

		if !errors.Is(err, ErrRcloneFailed) || result.Available {
			t.Fatalf("%s: got %+v %v", name, result, err)
		}
	}
}

func TestRcloneRC_itemWithoutRequestedHashIsUnavailableNotError(t *testing.T) {
	for name, body := range map[string]string{
		"no hashes":       `{"item":{"Name":"a.mkv","Hashes":{}}}`,
		"other hash only": `{"item":{"Name":"a.mkv","Hashes":{"sha1":"` + sha1Value + `"}}}`,
		"missing field":   `{"item":{"Name":"a.mkv"}}`,
		"empty value":     `{"item":{"Name":"a.mkv","Hashes":{"md5":""}}}`,
		"directory":       `{"item":{"Name":"a.mkv","IsDir":true}}`,
		"not found":       `{"item":null}`,
	} {
		server := newRCServer(t, respondWith(body))

		result, err := newRC(t, server.URL, 0).Hash(context.Background(), "/home/u/mnt/remote/a.mkv", MD5)

		if err != nil || result.Available {
			t.Fatalf("%s: got %+v %v", name, result, err)
		}
	}
}

func TestRcloneRC_non2xxIsTypedErrorWithoutEchoingBody(t *testing.T) {
	server := newRCServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, `{"error":"remote remote secret-bucket failed"}`)
	})

	_, err := newRC(t, server.withCredentials(), 0).Hash(context.Background(), "/home/u/mnt/remote/a.mkv", MD5)

	if !errors.Is(err, ErrRcloneFailed) || strings.Contains(err.Error(), "secret-bucket") || strings.Contains(err.Error(), rcPassword) {
		t.Fatalf("got %v", err)
	}
}

func TestRcloneRC_timeoutAndCancellation(t *testing.T) {
	release := make(chan struct{})
	server := newRCServer(t, func(_ http.ResponseWriter, _ *http.Request) { <-release })
	defer close(release)

	_, err := newRC(t, server.URL, 200*time.Millisecond).Hash(context.Background(), "/home/u/mnt/remote/a.mkv", MD5)
	if !errors.Is(err, ErrRcloneFailed) {
		t.Fatalf("timeout: got %v", err)
	}
	cancelled, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := newRC(t, server.URL, time.Minute).Hash(cancelled, "/home/u/mnt/remote/a.mkv", MD5); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel: got %v", err)
	}
}

func TestRcloneRC_appliesTheSamePathRulesAsExec(t *testing.T) {
	server := newRCServer(t, respondWith(`{"item":null}`))
	source := newRC(t, server.URL, 0)
	for path, expectedError := range map[string]error{
		"films/a.mkv":             ErrUnsafePath,
		"/home/u/mnt/remote/../x":   ErrUnsafePath,
		"/home/u/mnt/remote/a\x00b": ErrUnsafePath,
		"/home/u/mnt/remoteX/a.mkv": ErrNoMountForPath,
		"/etc/passwd":             ErrNoMountForPath,
	} {
		if _, err := source.Hash(context.Background(), path, MD5); !errors.Is(err, expectedError) {
			t.Fatalf("%q: got %v", path, err)
		}
	}
	if len(server.calls) != 0 {
		t.Fatal("rejected paths must not reach rclone")
	}
}

func TestNewRcloneRC_rejectsInvalidURLWithoutEchoingIt(t *testing.T) {
	for _, rawURL := range []string{"", "ftp://u:" + rcPassword + "@host:1", "http://", "://u:" + rcPassword + "@x", "rclone:5572"} {
		_, err := NewRcloneRC(RcOptions{URL: rawURL, Mounts: rcMounts})
		if err == nil || strings.Contains(err.Error(), rcPassword) {
			t.Fatalf("%q: got %v", rawURL, err)
		}
	}
}

func TestNewRclone_selectsRCThenBinaryThenUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	fake := newFakeRclone(t, "", 0)

	withRC, err := NewRclone(RcloneOptions{RCURL: "http://rclone:5572", Binary: fake.binary, Mounts: rcMounts})
	if err != nil {
		t.Fatal(err)
	}
	withBinary, err := NewRclone(RcloneOptions{Binary: fake.binary, Mounts: rcMounts})
	if err != nil {
		t.Fatal(err)
	}
	nothing, err := NewRclone(RcloneOptions{Mounts: rcMounts})
	if err != nil {
		t.Fatal(err)
	}

	if _, isRC := withRC.(*rcloneRC); !isRC {
		t.Fatalf("rc url must win, got %T", withRC)
	}
	if _, isExec := withBinary.(*rcloneExec); !isExec {
		t.Fatalf("binary expected, got %T", withBinary)
	}
	result, hashErr := nothing.Hash(context.Background(), "/home/u/mnt/remote/a.mkv", MD5)
	reasoned, hasReason := nothing.(interface{ Reason() string })
	if hashErr != nil || result.Available || !hasReason || reasoned.Reason() != ReasonNoRclone {
		t.Fatalf("fallback source: %+v %v %T", result, hashErr, nothing)
	}
}

func TestNewRcloneRC_neverPrintsCredentials(t *testing.T) {
	source := newRC(t, "http://rcuser:"+rcPassword+"@rclone:5572", 0)

	for _, rendered := range []string{fmtV(source), fmtPlus(source), fmtSharp(source)} {
		if strings.Contains(rendered, rcPassword) {
			t.Fatalf("credentials leaked: %s", rendered)
		}
	}
}

func fmtV(value any) string     { return fmt.Sprintf("%v", value) }
func fmtPlus(value any) string  { return fmt.Sprintf("%+v", value) }
func fmtSharp(value any) string { return fmt.Sprintf("%#v", value) }

func TestRcloneRC_defaultClientDisablesProxyAndRefusesRedirects(t *testing.T) {
	source := newRC(t, "http://rcuser:"+rcPassword+"@rclone:5572", 0).(*rcloneRC)

	transport, isTransport := source.client.Transport.(*http.Transport)

	if !isTransport || transport.Proxy != nil {
		t.Fatalf("expected an *http.Transport without proxy, got %T", source.client.Transport)
	}
	if err := source.client.CheckRedirect(nil, nil); !errors.Is(err, errRedirectRefused) {
		t.Fatalf("redirects must be refused, got %v", err)
	}
}

func TestRcloneRC_injectedClientTransportIsLeftUntouched(t *testing.T) {
	injectedTransport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	provided := &http.Client{Transport: injectedTransport}

	source, err := NewRcloneRC(RcOptions{URL: "http://rclone:5572", Mounts: rcMounts, HTTPClient: provided})
	if err != nil {
		t.Fatal(err)
	}

	used := source.(*rcloneRC).client
	if used.Transport != http.RoundTripper(injectedTransport) || injectedTransport.Proxy == nil || provided.CheckRedirect != nil {
		t.Fatal("the injected client must keep its transport and must not be modified")
	}
	if err := used.CheckRedirect(nil, nil); !errors.Is(err, errRedirectRefused) {
		t.Fatalf("redirects must still be refused, got %v", err)
	}
}

func TestRcloneRC_fallsBackToListWhenStatIsUnsupported(t *testing.T) {
	for name, statReply := range map[string]func(http.ResponseWriter){
		"404": func(writer http.ResponseWriter) { writer.WriteHeader(http.StatusNotFound) },
		"501": func(writer http.ResponseWriter) { writer.WriteHeader(http.StatusNotImplemented) },
		"unknown command": func(writer http.ResponseWriter) {
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(writer, `{"error":"Unknown command"}`)
		},
	} {
		server := newRCServer(t, func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/operations/stat" {
				statReply(writer)
				return
			}
			_, _ = io.WriteString(writer, `{"list":[{"Name":"b.mkv","Hashes":{"md5":"`+strings.Repeat("1", 32)+`"}},{"Name":"a.mkv","IsDir":false,"Hashes":{"md5":"`+md5Value+`"}}]}`)
		})

		result, err := newRC(t, server.URL, 0).Hash(context.Background(), "/home/u/mnt/remote/films/a.mkv", MD5)

		if err != nil || !result.Available || result.Value != md5Value {
			t.Fatalf("%s: %+v %v", name, result, err)
		}
		listCall := server.calls[len(server.calls)-1]
		expectedBody := `{"fs":"remote:bucket/prefix","opt":{"filesOnly":true,"hashTypes":["md5"],"noMimeType":true,"showHash":true},"remote":"films"}`
		gotBody, _ := json.Marshal(listCall.body)
		if listCall.path != "/operations/list" || string(gotBody) != expectedBody {
			t.Fatalf("%s: unexpected list call %s %s", name, listCall.path, gotBody)
		}
	}
}

func TestRcloneRC_listFallbackIsStrict(t *testing.T) {
	for name, listBody := range map[string]string{
		"file absent": `{"list":[{"Name":"b.mkv"}]}`,
		"no hash":     `{"list":[{"Name":"a.mkv","Hashes":{}}]}`,
		"empty list":  `{"list":[]}`,
	} {
		server := newRCServer(t, func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/operations/stat" {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(writer, listBody)
		})
		if result, err := newRC(t, server.URL, 0).Hash(context.Background(), "/home/u/mnt/remote/a.mkv", MD5); err != nil || result.Available {
			t.Fatalf("%s: %+v %v", name, result, err)
		}
	}
	bad := newRCServer(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/operations/stat" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(writer, `{"list":[{"Name":"a.mkv","Hashes":{"md5":"zz"}}]}`)
	})
	if _, err := newRC(t, bad.URL, 0).Hash(context.Background(), "/home/u/mnt/remote/a.mkv", MD5); !errors.Is(err, ErrRcloneFailed) {
		t.Fatalf("got %v", err)
	}
}

func TestRcloneRC_sizeMismatchWithLocalFileIsUnavailable(t *testing.T) {
	mountDirectory := t.TempDir()
	localPath := filepath.Join(mountDirectory, "a.mkv")
	if err := os.WriteFile(localPath, []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	for size, expectedAvailable := range map[int]bool{5: true, 9: false} {
		server := newRCServer(t, respondWith(fmt.Sprintf(`{"item":{"Name":"a.mkv","Size":%d,"Hashes":{"md5":"%s"}}}`, size, md5Value)))
		source, err := NewRcloneRC(RcOptions{URL: server.URL, Mounts: []Mount{{LocalPath: mountDirectory, Remote: "r:x"}}})
		if err != nil {
			t.Fatal(err)
		}

		result, err := source.Hash(context.Background(), localPath, MD5)

		if err != nil || result.Available != expectedAvailable {
			t.Fatalf("size %d: %+v %v", size, result, err)
		}
	}
}
