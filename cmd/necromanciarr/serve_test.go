package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
)

// pingCloser est le Closer d'un faux Service qui sait répondre à la sonde, comme *store.Store.
type pingCloser struct{ closed atomic.Bool }

func (closer *pingCloser) Close() error {
	closer.closed.Store(true)
	return nil
}

func (*pingCloser) Ping(context.Context) error { return nil }

// serveHarness remplace le Service et l'écoute de serve : l'écoute réelle est sur 127.0.0.1, port libre.
type serveHarness struct {
	closer    *pingCloser
	addresses chan string
}

func useServeHarness(t *testing.T) *serveHarness {
	t.Helper()
	harness := &serveHarness{closer: &pingCloser{}, addresses: make(chan string, 1)}
	previousService, previousListen := openService, listen
	openService = func(context.Context, config.Config) (app.Service, io.Closer, error) {
		return &fakeService{}, harness.closer, nil
	}
	listen = func(string) (net.Listener, error) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err == nil {
			harness.addresses <- listener.Addr().String()
		}
		return listener, err
	}
	t.Cleanup(func() { openService, listen = previousService, previousListen })
	return harness
}

type serveOutcome struct {
	exitCode       int
	stdout, stderr string
}

func startServe(ctx context.Context, args []string) <-chan serveOutcome {
	outcome := make(chan serveOutcome, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		exitCode := runServe(ctx, args, &stdout, &stderr)
		outcome <- serveOutcome{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
	}()
	return outcome
}

func probe(t *testing.T, address string) (int, string) {
	t.Helper()
	response, err := http.Get("http://" + address + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(body)
}

func waitOutcome(t *testing.T, outcome <-chan serveOutcome) serveOutcome {
	t.Helper()
	select {
	case result := <-outcome:
		return result
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop after the context was cancelled")
		return serveOutcome{}
	}
}

func TestServe_startsAndStopsOnContextCancel(t *testing.T) {
	harness := useServeHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outcome := startServe(ctx, []string{"--config-dir", t.TempDir()})
	address := <-harness.addresses
	gotStatus, gotBody := probe(t, address)
	cancel()
	result := waitOutcome(t, outcome)

	if gotStatus != http.StatusOK || gotBody != "ok" {
		t.Fatalf("gotValue %d %q, expectedValue 200 ok while serving", gotStatus, gotBody)
	}
	if result.exitCode != successExitCode || !strings.Contains(result.stdout, "listening on http://"+address) || result.stderr != "" {
		t.Fatalf("gotValue %+v, expectedValue exit 0 with the listening address and no warning", result)
	}
	if !harness.closer.closed.Load() {
		t.Fatal("gotValue the store left open, expectedValue it closed on shutdown")
	}
}

func TestServe_warnsWhenListeningOutsideLoopbackWithoutPrintingTheToken(t *testing.T) {
	const token = "serve-test-token-serve-test-token"
	harness := useServeHarness(t)
	previousLookup := lookupEnv
	lookupEnv = func(name string) (string, bool) {
		if name == "NECROMANCIARR_AUTH_TOKEN" {
			return token, true
		}
		return "", false
	}
	t.Cleanup(func() { lookupEnv = previousLookup })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	outcome := startServe(ctx, []string{"--config-dir", t.TempDir(), "--listen", "0.0.0.0:8787"})
	<-harness.addresses
	cancel()
	result := waitOutcome(t, outcome)

	if result.exitCode != successExitCode || !strings.Contains(result.stderr, nonLoopbackWarning) {
		t.Fatalf("gotValue %+v, expectedValue exit 0 and the non-loopback warning", result)
	}
	if strings.Contains(result.stdout+result.stderr, token) {
		t.Fatalf("gotValue %+v, expectedValue no token in the output", result)
	}
}

func TestServe_refusesANonLoopbackListenWithoutToken(t *testing.T) {
	useServeHarness(t)
	previousLookup := lookupEnv
	lookupEnv = func(string) (string, bool) { return "", false }
	t.Cleanup(func() { lookupEnv = previousLookup })
	var stdout, stderr bytes.Buffer

	exitCode := runServe(context.Background(), []string{"--config-dir", t.TempDir(), "--listen", "0.0.0.0:8787"}, &stdout, &stderr)

	refusal := stderr.String()
	namesTheFix := strings.Contains(refusal, "NECROMANCIARR_AUTH_TOKEN") && strings.Contains(refusal, "32") &&
		strings.Contains(refusal, "api-key")
	if exitCode == successExitCode || !namesTheFix {
		t.Fatalf("gotValue exit %d stderr %q, expectedValue a refusal naming the variable, 32 and api-key", exitCode, refusal)
	}
}

func TestHealthcheck_translatesWildcardListenToLoopback(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:8787":   "http://127.0.0.1:8787/healthz",
		"[::]:8787":      "http://127.0.0.1:8787/healthz",
		":8787":          "http://127.0.0.1:8787/healthz",
		"127.0.0.1:9000": "http://127.0.0.1:9000/healthz",
		"[::1]:8787":     "http://[::1]:8787/healthz",
		"localhost:8787": "http://localhost:8787/healthz",
	}
	for listenAddr, expectedValue := range cases {
		gotValue, err := healthcheckURL(listenAddr)
		if err != nil || gotValue != expectedValue {
			t.Fatalf("%s: gotValue %q %v, expectedValue %q", listenAddr, gotValue, err, expectedValue)
		}
	}
	if _, err := healthcheckURL("no-port"); err == nil {
		t.Fatal("gotValue nil, expectedValue an error for an address without port")
	}
}

func TestHealthcheck_exitsZeroOnlyWhenHealthzAnswers200(t *testing.T) {
	statuses := map[string]int{"ok": http.StatusOK, "unavailable": http.StatusServiceUnavailable, "redirect": http.StatusFound}
	for label, status := range statuses {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/healthz" {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			if status == http.StatusFound {
				http.Redirect(writer, request, "/elsewhere", status)
				return
			}
			writer.WriteHeader(status)
		}))
		address := strings.TrimPrefix(server.URL, "http://")
		expectedValue := failureExitCode
		if status == http.StatusOK {
			expectedValue = successExitCode
		}

		gotValue := runCli(t, t.TempDir(), "healthcheck", "--listen", address)

		server.Close()
		if gotValue.exitCode != expectedValue {
			t.Fatalf("%s: gotValue %+v, expectedValue exit %d", label, gotValue, expectedValue)
		}
	}
	unreachable := runCli(t, t.TempDir(), "healthcheck", "--listen", "127.0.0.1:1")
	if unreachable.exitCode != failureExitCode || unreachable.stderr != healthcheckFailure+"\n" {
		t.Fatalf("gotValue %+v, expectedValue exit 1 with only the failure line", unreachable)
	}
}
