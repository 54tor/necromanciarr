//go:build windows

package linker

import "golang.org/x/sys/windows"

// freeBytes lit la place disponible pour le compte courant sur le volume de directory.
func freeBytes(directory string) (uint64, error) {
	pathPointer, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return 0, err
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(pathPointer, &available, &total, &free); err != nil {
		return 0, err
	}
	return available, nil
}
