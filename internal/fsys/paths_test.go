package fsys_test

import (
	"path/filepath"
	"testing"

	"necromanciarr/internal/fsys"
)

// Sous Windows, filepath traiterait "\" comme séparateur et "C:" comme volume : RemotePaths doit les garder comme
// des caractères ordinaires d'un nom POSIX.
func TestPathSyntax_remoteAlwaysPOSIX(t *testing.T) {
	remote := fsys.RemotePaths()
	stringCases := []struct {
		name          string
		gotValue      string
		expectedValue string
	}{
		{"Join backslash", remote.Join("C:\\dir", "x"), "C:\\dir/x"},
		{"Join absolute", remote.Join("/srv", "media", "a\\b"), "/srv/media/a\\b"},
		{"Dir backslash", remote.Dir("a\\b"), "."},
		{"Dir posix", remote.Dir("/srv/media/a\\b"), "/srv/media"},
		{"Base backslash", remote.Base("C:\\dir\\file"), "C:\\dir\\file"},
		{"Clean", remote.Clean("/srv//media/../x\\..\\y/"), "/srv/x\\..\\y"},
		{"Clean drive", remote.Clean("C:/x/./y"), "C:/x/y"},
	}
	for _, testCase := range stringCases {
		if testCase.gotValue != testCase.expectedValue {
			t.Errorf("%s = %q, want %q", testCase.name, testCase.gotValue, testCase.expectedValue)
		}
	}
	for _, name := range []string{"C:\\dir", "C:/dir", "\\dir", "dir"} {
		if remote.IsAbs(name) {
			t.Errorf("IsAbs(%q) = true, want false", name)
		}
	}
	if !remote.IsAbs("/srv/media") {
		t.Errorf("IsAbs(/srv/media) = false, want true")
	}
}

func TestPathSyntax_remoteRel(t *testing.T) {
	remote := fsys.RemotePaths()
	cases := []struct{ base, target, expectedValue string }{
		{"/srv/media", "/srv/media/a\\b", "a\\b"},
		{"/srv/media", "/srv/media", "."},
		{"/srv/media/x", "/srv/links/y", "../../links/y"},
		{"/", "/srv", "srv"},
		{"/srv/", "/srv/media/", "media"},
		{"a", "..", "../.."},
		{"a/b", "a/c", "../c"},
	}
	for _, testCase := range cases {
		gotValue, err := remote.Rel(testCase.base, testCase.target)
		if err != nil || gotValue != testCase.expectedValue {
			t.Errorf("Rel(%q, %q) = %q, %v, want %q", testCase.base, testCase.target, gotValue, err, testCase.expectedValue)
		}
	}
	for _, refused := range [][2]string{{"/srv", "srv"}, {"srv", "/srv"}, {"..", "a"}, {"C:\\x", "/x"}} {
		if gotValue, err := remote.Rel(refused[0], refused[1]); err == nil {
			t.Errorf("Rel(%q, %q) = %q, want an error", refused[0], refused[1], gotValue)
		}
	}
}

func TestPathSyntax_localUsesFilepath(t *testing.T) {
	local := fsys.LocalPaths()
	expectedValue := filepath.Join("a", "b")
	gotValue := local.Join("a", "b")
	if gotValue != expectedValue {
		t.Errorf("Join = %q, want %q", gotValue, expectedValue)
	}
	if local.IsAbs("srv") {
		t.Errorf("IsAbs(srv) = true, want false")
	}
}
