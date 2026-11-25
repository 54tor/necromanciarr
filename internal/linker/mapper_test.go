package linker

import (
	"errors"
	"testing"

	"necromanciarr/internal/config"
)

func TestMapper_sftpRulesArePOSIXCaseSensitive(t *testing.T) {
	mapper := NewMapper([]config.PathRule{
		{From: "sftp://seedbox/srv/Links", To: "/downloads/links"},
		{From: "/srv/Links", To: "/local/links"},
	})
	translated := []struct{ original, valeurAttendue string }{
		{"sftp://seedbox/srv/Links/Movie [aa]/a.mkv", "/downloads/links/Movie [aa]/a.mkv"},
		{"sftp://seedbox/srv/Links", "/downloads/links"},
		{`sftp://seedbox/srv/Links/a\b.mkv`, `/downloads/links/a\b.mkv`},
		{"/srv/Links/a.mkv", "/local/links/a.mkv"},
	}
	for _, testCase := range translated {
		valeurObtenue, err := mapper.Translate(testCase.original)
		if err != nil || valeurObtenue != testCase.valeurAttendue {
			t.Fatalf("%s: valeurAttendue %s, valeurObtenue %q (err %v)", testCase.original, testCase.valeurAttendue, valeurObtenue, err)
		}
	}
	for _, original := range []string{
		"sftp://seedbox/srv/links/a.mkv",
		"sftp://seedbox/SRV/Links/a.mkv",
		"sftp://other/srv/Links/a.mkv",
		"sftp://seedbox/srv/Linksmore/a.mkv",
		"sftp:/seedbox/srv/Links/a.mkv",
		"sftp://seedbox/srv/Links/../Links/a.mkv",
	} {
		if valeurObtenue, err := mapper.Translate(original); !errors.Is(err, ErrNoRule) {
			t.Fatalf("%s: valeurAttendue ErrNoRule, valeurObtenue %q (err %v)", original, valeurObtenue, err)
		}
	}
}

func TestMapper_reverseRebuildsTheSFTPLocation(t *testing.T) {
	mapper := NewMapper([]config.PathRule{{From: "sftp://seedbox/srv/links", To: "/downloads/links"}})

	valeurObtenue, err := mapper.Reverse("/downloads/links/Movie/a.mkv")

	valeurAttendue := "sftp://seedbox/srv/links/Movie/a.mkv"
	if err != nil || valeurObtenue != valeurAttendue {
		t.Fatalf("valeurAttendue %s, valeurObtenue %q (err %v)", valeurAttendue, valeurObtenue, err)
	}
}
