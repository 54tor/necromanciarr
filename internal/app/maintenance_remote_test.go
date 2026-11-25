package app

import (
	"errors"
	"fmt"
	"testing"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/pipeline"
)

func TestClientView_remoteLinkPathNeedsAnSFTPRule(t *testing.T) {
	remotePath := "sftp://seedbox/srv/links/movie.mkv"
	unmappedViews := map[string]clientView{
		"no mapper":       {},
		"local rule only": {mapper: linker.NewMapper([]config.PathRule{{From: "/srv/links", To: "/downloads/links"}})},
		"other provider":  {mapper: linker.NewMapper([]config.PathRule{{From: "sftp://other/srv/links", To: "/downloads/links"}})},
	}
	for name, view := range unmappedViews {
		t.Run(name, func(t *testing.T) {
			_, err := view.clientPath(remotePath)

			if !errors.Is(err, ErrClientPathUnmapped) {
				t.Fatalf("gotValue %v, expectedValue ErrClientPathUnmapped, never the same path", err)
			}
		})
	}
	mapped := clientView{mapper: linker.NewMapper([]config.PathRule{{From: "sftp://seedbox/srv/links", To: "/downloads/links"}})}
	if gotValue, err := mapped.clientPath(remotePath); err != nil || gotValue != "/downloads/links/movie.mkv" {
		t.Fatalf("gotValue %q %v, expectedValue the translated client path", gotValue, err)
	}
	if gotValue, err := (clientView{}).clientPath("/srv/links/movie.mkv"); err != nil || gotValue != "/srv/links/movie.mkv" {
		t.Fatalf("gotValue %q %v, expectedValue the same local path (unchanged)", gotValue, err)
	}
}

func TestClientPathUnmapped_reusesTheSeedReasonCode(t *testing.T) {
	if gotValue := fmt.Sprint(ErrClientPathUnmapped); gotValue != "app: "+pipeline.ReasonClientPathUnmapped {
		t.Fatalf("gotValue %q, expectedValue the stable code %s", gotValue, pipeline.ReasonClientPathUnmapped)
	}
}

func TestDedupeReasonCode_noAtomicReplace(t *testing.T) {
	if gotValue := dedupeReasonCode(fmt.Errorf("plan: %w", fsys.ErrNoAtomicReplace)); gotValue != DedupeReasonNoAtomicReplace {
		t.Fatalf("gotValue %q, expectedValue %q", gotValue, DedupeReasonNoAtomicReplace)
	}
}
