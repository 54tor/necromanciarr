//go:build windows

package local

import (
	"os"
	"path/filepath"
	"sync"

	"necromanciarr/internal/fsys"
)

// platformAtomicReplace : os.Rename passe par MoveFileEx(MOVEFILE_REPLACE_EXISTING), le remplacement qu'utilise
// déjà dedupe sous Windows.
const platformAtomicReplace = true

// symlinkAllowed sonde une fois le droit de créer un lien symbolique (privilège ou mode développeur), propre au
// compte et non au dossier.
var symlinkAllowed = sync.OnceValue(func() bool {
	probeDir, err := os.MkdirTemp("", "necromanciarr-symlink-probe-")
	if err != nil {
		return false
	}
	defer func() { _ = os.RemoveAll(probeDir) }()
	return os.Symlink("target", filepath.Join(probeDir, "link")) == nil
})

// platformCapabilities : Windows ne donne pas d'inode par FileInfo, ni de reflink.
func platformCapabilities() fsys.Capabilities {
	capabilities := fsys.Capabilities{
		VolumeIdentity: true, Hardlink: true, AtomicReplace: platformAtomicReplace, Symlink: symlinkAllowed(),
		Reasons: map[string]string{
			reasonKeyServerCopy:   reasonLocalVolume,
			reasonKeyFileIdentity: reasonNoFileIndex,
			reasonKeyReflink:      reasonUnsupportedPlatform,
		},
	}
	if !capabilities.Symlink {
		capabilities.Reasons[reasonKeySymlink] = reasonSymlinkPrivilegeMissing
	}
	return capabilities
}
