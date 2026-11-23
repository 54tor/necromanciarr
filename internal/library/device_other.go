//go:build !linux

package library

import "io/fs"

func deviceOf(info fs.FileInfo) (uint64, bool) {
	return 0, false
}
