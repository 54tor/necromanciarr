package crossseed

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestResolveMode_table(t *testing.T) {
	cases := []struct {
		mode                  string
		configured, reachable bool
		expectedBehavior      Behavior
		expectWarning         bool
	}{
		{"auto", true, true, Delegate, false},
		{"auto", true, false, Native, true},
		{"auto", false, false, Native, false},
		{"auto", false, true, Native, false},
		{"", true, true, Delegate, false},
		{"AUTO", true, true, Delegate, false},
		{"delegate", true, true, Delegate, false},
		{"delegate", true, false, Disabled, true},
		{"delegate", false, false, Disabled, true},
		{"Delegate", false, true, Disabled, true},
		{"native", true, true, Native, false},
		{"native", false, false, Native, false},
		{"NATIVE", true, false, Native, false},
		{"off", true, true, Disabled, false},
		{"off", false, false, Disabled, false},
		{" Off ", true, false, Disabled, false},
	}
	for _, testCase := range cases {
		gotBehavior, gotWarning, err := ResolveMode(testCase.mode, testCase.configured, testCase.reachable)
		if err != nil {
			t.Fatalf("%+v: %v", testCase, err)
		}
		if gotBehavior != testCase.expectedBehavior || (gotWarning != "") != testCase.expectWarning {
			t.Fatalf("%+v: gotValue %v %q", testCase, gotBehavior, gotWarning)
		}
	}
}

func TestResolveMode_unknownModeListsAllowed(t *testing.T) {
	_, _, err := ResolveMode("turbo", true, true)

	if err == nil {
		t.Fatal("expected an error")
	}
	for _, allowed := range []string{"auto", "delegate", "native", "off"} {
		if !strings.Contains(err.Error(), allowed) {
			t.Fatalf("%q missing from %v", allowed, err)
		}
	}
}

func TestReachable_trueWhenPortOpenFalseWhenClosedAndNeverSendsData(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan int, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			received <- -1
			return
		}
		defer func() { _ = connection.Close() }()
		_ = connection.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		buffer := make([]byte, 16)
		count, _ := connection.Read(buffer)
		received <- count
	}()
	openURL := "http://" + listener.Addr().String()

	if !Reachable(context.Background(), openURL, 0) {
		t.Fatal("open port reported unreachable")
	}
	if count := <-received; count != 0 {
		t.Fatalf("%d bytes sent by the probe", count)
	}
	_ = listener.Close()
	if Reachable(context.Background(), openURL, time.Second) {
		t.Fatal("closed port reported reachable")
	}
	if Reachable(context.Background(), "::not a url::", time.Second) {
		t.Fatal("invalid url reported reachable")
	}
}

func TestReachable_usesSchemeDefaultPortAndHonoursContext(t *testing.T) {
	var dialed string
	dial := func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = address
		client, server := net.Pipe()
		_ = server.Close()
		return client, nil
	}

	if !reachableWith(context.Background(), "https://example.com", time.Second, dial) || dialed != "example.com:443" {
		t.Fatalf("dialed %q", dialed)
	}
	reachableWith(context.Background(), "http://example.com/x", time.Second, dial)
	if dialed != "example.com:80" {
		t.Fatalf("dialed %q", dialed)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	blocking := func(ctx context.Context, _, _ string) (net.Conn, error) { return nil, ctx.Err() }
	if reachableWith(cancelled, "http://example.com", time.Second, blocking) {
		t.Fatal("cancelled context reported reachable")
	}
}
