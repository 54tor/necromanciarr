package linker

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteVerifiedFile_unreadableExistingAnnexKeepsTheReadError(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions enforced for the test account")
	}
	root := t.TempDir()
	existing := filepath.Join(root, "Movie.2020.nfo")
	writeFile(t, existing, verifiedContent)
	if err := os.Chmod(existing, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(existing, 0o600) }) // #nosec G302 -- remise des droits du fichier de test

	err := WriteVerifiedFile(openRoot(t, root), "Movie.2020.nfo", verifiedContent)

	if !errors.Is(err, fs.ErrPermission) || errors.Is(err, ErrDestinationExists) {
		t.Fatalf("expectedValue the raw permission error, gotValue %v", err)
	}
}
