//go:build unix && !linux

package local

import "os"

// isMountRoot : hors Linux, seul le changement de périphérique révèle un point de montage.
func isMountRoot(*os.Root) (bool, error) {
	return false, nil
}
