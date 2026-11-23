//go:build !linux

package library

import "io/fs"

// deviceOf ne sait pas lire le périphérique hors Linux : la détection des montages est alors désactivée.
func deviceOf(info fs.FileInfo) (uint64, bool) {
	return 0, false
}
