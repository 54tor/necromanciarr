//go:build !linux

package local

import "io/fs"

// DeviceOf ne sait pas lire le périphérique hors Linux : la détection des montages est alors désactivée.
func DeviceOf(info fs.FileInfo) (uint64, bool) {
	return 0, false
}
