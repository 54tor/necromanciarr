//go:build windows

package local

import "testing"

func TestSymlinkTarget_prefixesAbsoluteWindowsPathsOnly(t *testing.T) {
	cases := map[string]string{
		`C:\media\movie.mkv`:     `\??\C:\media\movie.mkv`,
		`C:/media/movie.mkv`:     `\??\C:\media\movie.mkv`,
		`\\nas\share\movie.mkv`:  `\??\UNC\nas\share\movie.mkv`,
		`\\?\C:\media\movie.mkv`: `\??\C:\media\movie.mkv`,
		`\??\C:\media\movie.mkv`: `\??\C:\media\movie.mkv`,
		`..\library\movie.mkv`:   `..\library\movie.mkv`,
		`movie.mkv`:              `movie.mkv`,
	}
	for target, valeurAttendue := range cases {
		if valeurObtenue := symlinkTarget(target); valeurObtenue != valeurAttendue {
			t.Errorf("symlinkTarget(%q) valeurObtenue %q, valeurAttendue %q", target, valeurObtenue, valeurAttendue)
		}
	}
}
