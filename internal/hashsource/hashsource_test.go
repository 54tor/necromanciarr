package hashsource

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	md5Value  = "d41d8cd98f00b204e9800998ecf8427e"
	sha1Value = "da39a3ee5e6b4b0d3255bfef95601890afd80709"
)

var testMounts = []Mount{
	{LocalPath: "/home/u/mnt/remote", Remote: "remote:media"},
	{LocalPath: "/home/u/mnt/remote/special", Remote: "other:x"},
}

// fakeRclone est un faux binaire : il enregistre ses arguments et affiche une sortie fixe.
type fakeRclone struct {
	binary   string
	argsFile string
	pidFile  string
}

func newFakeRclone(t *testing.T, stdout string, exitCode int) fakeRclone {
	t.Helper()
	return writeFakeRclone(t, "cat '%OUT%'\nexit "+itoa(exitCode)+"\n", stdout)
}

func itoa(number int) string { return strconv.Itoa(number) }

func writeFakeRclone(t *testing.T, body, stdout string) fakeRclone {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake rclone is a POSIX shell script")
	}
	directory := t.TempDir()
	fake := fakeRclone{binary: filepath.Join(directory, "rclone"), argsFile: filepath.Join(directory, "args"), pidFile: filepath.Join(directory, "pid")}
	outputFile := filepath.Join(directory, "out")
	if err := os.WriteFile(outputFile, []byte(stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + fake.argsFile + "'\necho $$ > '" + fake.pidFile + "'\n" +
		strings.ReplaceAll(body, "%OUT%", outputFile)
	if err := os.WriteFile(fake.binary, []byte(script), 0o700); err != nil { // #nosec G306 -- faux exécutable de test
		t.Fatal(err)
	}
	return fake
}

func (fake fakeRclone) arguments(t *testing.T) []string {
	t.Helper()
	content, err := os.ReadFile(fake.argsFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
}

func (fake fakeRclone) source(t *testing.T, timeout time.Duration) HashSource {
	t.Helper()
	source, err := NewRcloneExec(ExecOptions{Binary: fake.binary, Mounts: testMounts, Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestRclone_translatesLocalPathToRemoteOnSegmentBoundary(t *testing.T) {
	cases := []struct {
		path, expectedRemote string
	}{
		{"/home/u/mnt/remote/films/a.mkv", "remote:media/films/a.mkv"},
		{"/home/u/mnt/remote/special/b.mkv", "other:x/b.mkv"},
	}
	for _, testCase := range cases {
		fake := newFakeRclone(t, md5Value+"  "+filepath.Base(testCase.path)+"\n", 0)

		result, err := fake.source(t, 0).Hash(context.Background(), testCase.path, MD5)

		if err != nil || !result.Available || result.Value != md5Value || result.Origin != Backend {
			t.Fatalf("%s: %+v %v", testCase.path, result, err)
		}
		expectedValue := []string{"hashsum", "MD5", "--", testCase.expectedRemote}
		if gotValue := fake.arguments(t); !slices.Equal(gotValue, expectedValue) {
			t.Fatalf("got %v, expected %v", gotValue, expectedValue)
		}
	}
}

func TestRclone_neverPassesDownloadAndUsesDoubleDash(t *testing.T) {
	fake := newFakeRclone(t, md5Value+"  --download\n", 0)

	result, err := fake.source(t, 0).Hash(context.Background(), "/home/u/mnt/remote/--download", MD5)

	if err != nil || !result.Available {
		t.Fatalf("%+v %v", result, err)
	}
	arguments := fake.arguments(t)
	separatorIndex := slices.Index(arguments, "--")
	if separatorIndex != 2 || arguments[3] != "remote:media/--download" || len(arguments) != 4 {
		t.Fatalf("unexpected arguments %v", arguments)
	}
	if slices.ContainsFunc(arguments[:separatorIndex], func(argument string) bool { return strings.Contains(argument, "download") }) {
		t.Fatalf("download flag passed: %v", arguments)
	}
}

func TestRclone_emptyOutputMeansUnavailableNotError(t *testing.T) {
	fake := newFakeRclone(t, "", 0)

	result, err := fake.source(t, 0).Hash(context.Background(), "/home/u/mnt/remote/a.mkv", MD5)

	if err != nil || result.Available {
		t.Fatalf("got %+v %v", result, err)
	}
}

func TestRclone_rejectsUnsafeOrUnmountedPaths(t *testing.T) {
	cases := []struct {
		name, path    string
		expectedError error
	}{
		{"empty", "", ErrUnsafePath},
		{"relative", "films/a.mkv", ErrUnsafePath},
		{"parent segment", "/home/u/mnt/remote/../../etc/passwd", ErrUnsafePath},
		{"NUL byte", "/home/u/mnt/remote/a\x00b", ErrUnsafePath},
		{"not cleaned", "/home/u/mnt/remote//a.mkv", ErrUnsafePath},
		{"trailing slash", "/home/u/mnt/remote/films/", ErrUnsafePath},
		{"outside any mount", "/etc/passwd", ErrNoMountForPath},
		{"segment boundary", "/home/u/mnt/remoteX/a.mkv", ErrNoMountForPath},
		{"mount root itself", "/home/u/mnt/remote", ErrNoMountForPath},
	}
	fake := newFakeRclone(t, md5Value+"  a.mkv\n", 0)
	for _, testCase := range cases {
		_, err := fake.source(t, 0).Hash(context.Background(), testCase.path, MD5)

		if !errors.Is(err, testCase.expectedError) {
			t.Fatalf("%s: got %v, expected %v", testCase.name, err, testCase.expectedError)
		}
	}
	if _, statErr := os.Stat(fake.argsFile); statErr == nil {
		t.Fatal("rclone must not run for a rejected path")
	}
}

func TestRclone_rejectsUnknownAlgorithm(t *testing.T) {
	fake := newFakeRclone(t, "", 0)

	_, err := fake.source(t, 0).Hash(context.Background(), "/home/u/mnt/remote/a.mkv", Algorithm("md5; reboot"))

	if !errors.Is(err, ErrUnknownAlgorithm) {
		t.Fatalf("got %v", err)
	}
}

func TestNewRcloneExec_rejectsInvalidMounts(t *testing.T) {
	for _, mount := range []Mount{{"relative", "remote:x"}, {"/home/u/a", "noColon"}, {"/home/u/a", "-flag:x"}, {"/home/u/../a", "m:x"}} {
		if _, err := NewRcloneExec(ExecOptions{Mounts: []Mount{mount}}); err == nil {
			t.Fatalf("mount %+v accepted", mount)
		}
	}
}

func TestRclone_capsStdout(t *testing.T) {
	huge := strings.Repeat(md5Value+"  other.mkv\n", 5000)
	fake := newFakeRclone(t, huge, 0)

	_, err := fake.source(t, 0).Hash(context.Background(), "/home/u/mnt/remote/a.mkv", MD5)

	if !errors.Is(err, ErrRcloneFailed) {
		t.Fatalf("got %v", err)
	}
}

func TestRclone_parsesHostileOutputStrictly(t *testing.T) {
	cases := []struct {
		name, output      string
		expectedAvailable bool
		expectedFailure   bool
	}{
		{"hash of another file only", md5Value + "  other.mkv\n", false, false},
		{"unsupported hash", "UNSUPPORTED  a.mkv\n", false, false},
		{"blank hash", "  a.mkv\n", false, false},
		{"upper case hex accepted", strings.ToUpper(md5Value) + "  a.mkv\n", true, false},
		{"not hex", strings.Repeat("z", 32) + "  a.mkv\n", false, true},
		{"wrong length", sha1Value + "  a.mkv\n", false, true},
		{"no separator", md5Value + " a.mkv\n", false, true},
		{"injected garbage line", "ERROR: boom\n", false, true},
		{"conflicting duplicates", md5Value + "  a.mkv\n" + strings.Repeat("1", 32) + "  a.mkv\n", false, true},
		{"mixed good and bad lines", md5Value + "  a.mkv\nnot a hash line\n", false, true},
	}
	for _, testCase := range cases {
		fake := newFakeRclone(t, testCase.output, 0)

		result, err := fake.source(t, 0).Hash(context.Background(), "/home/u/mnt/remote/a.mkv", MD5)

		if testCase.expectedFailure != errors.Is(err, ErrRcloneFailed) || result.Available != testCase.expectedAvailable {
			t.Fatalf("%s: got %+v %v", testCase.name, result, err)
		}
	}
}

func TestRclone_failureDoesNotLeakOutput(t *testing.T) {
	fake := writeFakeRclone(t, "echo 'SECRET-TOKEN on stdout'\necho 'SECRET-TOKEN on stderr' >&2\nexit 3\n", "")

	_, err := fake.source(t, 0).Hash(context.Background(), "/home/u/mnt/remote/a.mkv", MD5)

	if !errors.Is(err, ErrRcloneFailed) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("got %v", err)
	}
}

func TestRclone_timeoutKillsTheProcessWithoutLeakingOutput(t *testing.T) {
	fake := writeFakeRclone(t, "echo 'SECRET-TOKEN'\nexec sleep 30\n", "")
	started := time.Now()

	_, err := fake.source(t, 300*time.Millisecond).Hash(context.Background(), "/home/u/mnt/remote/a.mkv", MD5)

	if !errors.Is(err, ErrRcloneFailed) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("got %v", err)
	}
	if time.Since(started) > 10*time.Second {
		t.Fatalf("timeout not enforced: %s", time.Since(started))
	}
	assertProcessGone(t, fake.pidFile)
}

func assertProcessGone(t *testing.T, pidFile string) {
	t.Helper()
	content, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	for _, digit := range strings.TrimSpace(string(content)) {
		pid = pid*10 + int(digit-'0')
	}
	process, _ := os.FindProcess(pid)
	if err := process.Signal(syscall.Signal(0)); err == nil {
		t.Fatalf("process %d still alive", pid)
	}
}

func TestRclone_cancelledContextKillsTheProcess(t *testing.T) {
	fake := writeFakeRclone(t, "exec sleep 30\n", "")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	_, err := fake.source(t, time.Minute).Hash(ctx, "/home/u/mnt/remote/a.mkv", MD5)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	assertProcessGone(t, fake.pidFile)
}

type countingLimiter struct{ totalBytes int64 }

func (limiter *countingLimiter) WaitN(_ context.Context, byteCount int) error {
	limiter.totalBytes += int64(byteCount)
	return nil
}

func TestLocal_hashesStreamedAndRateLimited(t *testing.T) {
	content := make([]byte, 200*1024+17)
	for index := range content {
		content[index] = byte(index * 7)
	}
	path := filepath.Join(t.TempDir(), "a.bin")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	md5Sum, sha1Sum, sha256Sum := md5.Sum(content), sha1.Sum(content), sha256.Sum256(content)
	expectedValues := map[Algorithm]string{
		MD5: hex.EncodeToString(md5Sum[:]), SHA1: hex.EncodeToString(sha1Sum[:]), SHA256: hex.EncodeToString(sha256Sum[:]),
	}
	for algorithm, expectedValue := range expectedValues {
		limiter := &countingLimiter{}

		result, err := NewLocal(limiter).Hash(context.Background(), path, algorithm)

		if err != nil || !result.Available || result.Origin != ReadLocally || result.Value != expectedValue {
			t.Fatalf("%s: %+v %v", algorithm, result, err)
		}
		if limiter.totalBytes != int64(len(content)) {
			t.Fatalf("%s: limiter charged %d for %d bytes", algorithm, limiter.totalBytes, len(content))
		}
	}
}

func TestLocal_honoursContextAndRejectsUnknownAlgorithm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.bin")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := NewLocal(nil).Hash(cancelled, path, MD5); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if _, err := NewLocal(nil).Hash(context.Background(), path, "CRC"); !errors.Is(err, ErrUnknownAlgorithm) {
		t.Fatalf("got %v", err)
	}
}
