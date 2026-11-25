package fsys_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"necromanciarr/internal/fsys"
)

func localAbsolute() string {
	return filepath.Join(filepath.VolumeName(mustAbs(".")), string(filepath.Separator), "srv", "media")
}

func mustAbs(name string) string {
	absolute, err := filepath.Abs(name)
	if err != nil {
		panic(err)
	}
	return absolute
}

func TestParseLocation_sftpAndLocal(t *testing.T) {
	cases := []struct {
		raw           string
		expectedValue fsys.Location
	}{
		{"sftp://seedbox/srv/media", fsys.Location{Volume: "seedbox", Path: "/srv/media"}},
		{"sftp://seedbox/", fsys.Location{Volume: "seedbox", Path: "/"}},
		{"sftp://seed-box-2/srv//media/./x/", fsys.Location{Volume: "seed-box-2", Path: "/srv/media/x"}},
		{localAbsolute(), fsys.Location{Volume: "", Path: localAbsolute()}},
	}
	for _, testCase := range cases {
		gotValue, err := fsys.ParseLocation(testCase.raw)
		if err != nil {
			t.Fatalf("ParseLocation(%q): %v", testCase.raw, err)
		}
		if gotValue != testCase.expectedValue {
			t.Errorf("ParseLocation(%q) = %+v, want %+v", testCase.raw, gotValue, testCase.expectedValue)
		}
	}
}

func TestParseLocation_refusesMalformed(t *testing.T) {
	refused := []string{
		"",
		"sftp://",
		"sftp://seedbox",
		"sftp:///srv/media",
		"sftp://x/../y",
		"sftp://x/a/..",
		"sftp://Seedbox/srv",
		"sftp://seed_box/srv",
		"sftp://alice@seedbox/srv",
		"sftp://seedbox:22/srv",
		"sftp://seedbox/srv\x00media",
		"sftp://seedbox/srv\nmedia",
		"sftp://seedbox/srv\x7fmedia",
		"relative/path",
		"./media",
		"srv\x00media",
		"sftp://seedbox/" + strings.Repeat("a", 4096),
	}
	for _, raw := range refused {
		_, err := fsys.ParseLocation(raw)
		if !errors.Is(err, fsys.ErrInvalidLocation) {
			t.Errorf("ParseLocation(%q) error = %v, want ErrInvalidLocation", raw, err)
		}
	}
}

func TestParseLocation_errorNeverEchoesInput(t *testing.T) {
	const secretLookingInput = "sftp://seedbox/home/u/../private"
	_, err := fsys.ParseLocation(secretLookingInput)
	if err == nil || strings.Contains(err.Error(), "/home/u") {
		t.Fatalf("error = %v, want a refusal without the input path", err)
	}
}

func TestLocation_StringRoundTrips(t *testing.T) {
	for _, raw := range []string{"sftp://seedbox/srv/media", "sftp://seedbox/", localAbsolute()} {
		location, err := fsys.ParseLocation(raw)
		if err != nil {
			t.Fatalf("ParseLocation(%q): %v", raw, err)
		}
		expectedValue := raw
		gotValue := location.String()
		if gotValue != expectedValue {
			t.Errorf("String() = %q, want %q", gotValue, expectedValue)
		}
		reparsed, err := fsys.ParseLocation(gotValue)
		if err != nil || reparsed != location {
			t.Errorf("ParseLocation(String()) = %+v, %v, want %+v", reparsed, err, location)
		}
	}
}

func FuzzParseLocation(f *testing.F) {
	seeds := []string{
		"sftp://seedbox/srv/media", "sftp://", "sftp:///x", "sftp://x/../y", "sftp://x//y/", "/srv/media",
		"C:\\srv\\media", "\\\\server\\share", "sftp://x/\x00", "sftp://\xff/x", "sftp://x/\xff", "sftp:/x",
		"sftp://-/x", "sftp://x/%2e%2e/y", "sftp://x/a\\..\\b",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		location, err := fsys.ParseLocation(raw)
		if err != nil {
			if !errors.Is(err, fsys.ErrInvalidLocation) {
				t.Fatalf("untyped error %v", err)
			}
			return
		}
		reparsed, err := fsys.ParseLocation(location.String())
		if err != nil || reparsed != location {
			t.Fatalf("round trip of %q: %+v, %v, want %+v", raw, reparsed, err, location)
		}
		if location.Volume != "" && strings.Contains("/"+location.Path+"/", "/../") {
			t.Fatalf("remote path with .. accepted: %q", raw)
		}
	})
}
