//go:build unix && !linux

package local

import "os"

// isMountRoot : hors Linux, seul le changement de périphérique révèle un point de montage.
func isMountRoot(*os.Root) (bool, error) {
	return false, nil
}

// recordMountRootGap : hors Linux, aucun montage lié du même système de fichiers n'est détecté.
func recordMountRootGap(reasons map[string]string) {
	reasons[reasonKeyMountRoot] = reasonUnsupportedPlatform
}
