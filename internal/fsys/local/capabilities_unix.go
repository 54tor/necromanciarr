//go:build unix

package local

import (
	"runtime"

	"necromanciarr/internal/fsys"
)

// platformAtomicReplace : rename(2) remplace atomiquement une entrée du même système de fichiers.
const platformAtomicReplace = true

// platformCapabilities : l'identité de fichier n'est annoncée que là où FilesystemKindOf sait classer un volume
// (Linux) ; ailleurs FileID rend toujours known=false et l'annoncer ferait échouer le registre.
func platformCapabilities() fsys.Capabilities {
	isLinux := runtime.GOOS == "linux"
	capabilities := fsys.Capabilities{
		FileIdentity: isLinux, VolumeIdentity: true,
		Symlink: true, Hardlink: true, AtomicReplace: platformAtomicReplace, Reflink: isLinux,
		Reasons: map[string]string{reasonKeyServerCopy: reasonLocalVolume},
	}
	if !isLinux {
		capabilities.Reasons[reasonKeyFileIdentity] = reasonFilesystemUnclassified
		capabilities.Reasons[reasonKeyReflink] = reasonUnsupportedPlatform
	}
	return capabilities
}
