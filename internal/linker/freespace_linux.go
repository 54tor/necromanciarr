//go:build linux

package linker

import "golang.org/x/sys/unix"

// freeBytes lit la place disponible pour un compte non privilégié sur le volume de directory (statfs).
func freeBytes(directory string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(directory, &stat); err != nil {
		return 0, err
	}
	return stat.Bavail * uint64(stat.Bsize), nil // #nosec G115 -- la taille de bloc est positive
}
