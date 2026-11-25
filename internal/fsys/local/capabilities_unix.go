//go:build unix

package local

import (
	"runtime"

	"necromanciarr/internal/fsys"
)

// platformAtomicReplace : rename(2) remplace atomiquement une entrée du même système de fichiers.
const platformAtomicReplace = true

// platformCapabilities : FileIdentity au sens du registre (Stat_t partout sous Unix) ; l'identité stricte est
// ReliableFileID.
func platformCapabilities() fsys.Capabilities {
	isLinux := runtime.GOOS == "linux"
	capabilities := fsys.Capabilities{
		FileIdentity: true, VolumeIdentity: true,
		Symlink: true, Hardlink: true, AtomicReplace: platformAtomicReplace, Reflink: isLinux,
		Reasons: map[string]string{reasonKeyServerCopy: reasonLocalVolume},
	}
	if !isLinux {
		capabilities.Reasons[reasonKeyReflink] = reasonUnsupportedPlatform
	}
	return capabilities
}
