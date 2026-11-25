//go:build linux

package local

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// isMountRoot lit STATX_ATTR_MOUNT_ROOT sur le handle du dossier : un montage lié du même système de fichiers
// garde le périphérique, seul cet attribut le révèle. Un noyau sans statx (avant 4.11) ou sans l'attribut
// (avant 5.8) rend false : seul le contrôle du périphérique reste alors.
func isMountRoot(dir *os.Root) (bool, error) {
	handle, err := dir.Open(".")
	if err != nil {
		return false, err
	}
	defer func() { _ = handle.Close() }()
	var statx unix.Statx_t
	err = unix.Statx(int(handle.Fd()), "", unix.AT_EMPTY_PATH, unix.STATX_TYPE, &statx) // #nosec G115 -- un descripteur tient dans un int
	if errors.Is(err, unix.ENOSYS) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	isAttributeKnown := statx.Attributes_mask&unix.STATX_ATTR_MOUNT_ROOT != 0
	return isAttributeKnown && statx.Attributes&unix.STATX_ATTR_MOUNT_ROOT != 0, nil
}
