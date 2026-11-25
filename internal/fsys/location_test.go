package fsys_test

import (
	"errors"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"necromanciarr/internal/fsys"
)

func localAbsolute() string {
	return filepath.Join(filepath.VolumeName(mustAbs(".")), string(filepath.Separator), "srv", "media")
}

var separator = string(filepath.Separator)

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
		{localAbsolute() + separator, fsys.Location{Volume: "", Path: localAbsolute()}},
		{localAbsolute() + separator + "." + separator + "x", fsys.Location{Volume: "", Path: filepath.Join(localAbsolute(), "x")}},
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
		localAbsolute() + separator + ".." + separator + "media",
		localAbsolute() + separator + "..",
		"sftp://seedbox/srv\u0085media",
		"sftp://seedbox/srv\u009fmedia",
		"sftp://seedbox/srv\u202emedia",
		"sftp://seedbox/srv\u2066media",
		"sftp://seedbox/srv\u200emedia",
		"sftp://seedbox/srv\xffmedia",
		localAbsolute() + "\u202e",
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

var volumeNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

func FuzzParseLocation(f *testing.F) {
	seeds := []string{
		"sftp://seedbox/srv/media", "sftp://", "sftp:///x", "sftp://x/../y", "sftp://x//y/", "/srv/media",
		"C:\\srv\\media", "\\\\server\\share", "sftp://x/\x00", "sftp://\xff/x", "sftp://x/\xff", "sftp:/x",
		"sftp://-/x", "sftp://x/%2e%2e/y", "sftp://x/a\\..\\b", "/srv/../x", "/srv/media/", "sftp://x/\u202e",
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
		if location.Volume == "" {
			if location.Path != filepath.Clean(location.Path) || !filepath.IsAbs(location.Path) {
				t.Fatalf("local path not absolute and clean: %q", location.Path)
			}
			return
		}
		if !volumeNamePattern.MatchString(location.Volume) {
			t.Fatalf("volume name %q accepted", location.Volume)
		}
		if location.Path != path.Clean(location.Path) || !path.IsAbs(location.Path) {
			t.Fatalf("remote path not absolute and clean: %q", location.Path)
		}
	})
}

func TestLocation_overlapsComparesPerVolumeWithoutTouchingTheDisk(t *testing.T) {
	cases := []struct {
		name            string
		first, second   string
		expectedOverlap bool
	}{
		{"local equal", "/srv/media", "/srv/media/", true},
		{"local parent", "/srv", "/srv/media", true},
		{"local child", "/srv/media/tv", "/srv/media", true},
		{"local siblings", "/srv/media", "/srv/links", false},
		{"local shared prefix is not a parent", "/data/tv", "/data/tvshows", false},
		{"remote equal", "sftp://seedbox/srv/media", "sftp://seedbox/srv/media", true},
		{"remote parent", "sftp://seedbox/srv", "sftp://seedbox/srv/media", true},
		{"remote child", "sftp://seedbox/srv/media/tv", "sftp://seedbox/srv/media", true},
		{"remote shared prefix is not a parent", "sftp://seedbox/data/tv", "sftp://seedbox/data/tvshows", false},
		{"same path on two remote volumes", "sftp://seedbox/srv/media", "sftp://other/srv/media", false},
		{"remote root against a nested path of another volume", "sftp://seedbox/", "sftp://other/srv", false},
		{"same path on local and remote", "/srv/media", "sftp://seedbox/srv/media", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			first, err := fsys.ParseLocation(portableLocation(testCase.first))
			if err != nil {
				t.Fatal(err)
			}
			second, err := fsys.ParseLocation(portableLocation(testCase.second))
			if err != nil {
				t.Fatal(err)
			}

			valeurObtenue := first.Overlaps(second) && second.Overlaps(first)
			if valeurObtenue != testCase.expectedOverlap {
				t.Fatalf("valeurAttendue %v, valeurObtenue %v", testCase.expectedOverlap, valeurObtenue)
			}
		})
	}
}

// portableLocation donne à un chemin local de test son lecteur sous Windows ; une adresse sftp:// reste telle quelle.
func portableLocation(raw string) string {
	if strings.HasPrefix(raw, "sftp://") {
		return raw
	}
	return filepath.VolumeName(mustAbs(".")) + filepath.FromSlash(raw)
}
