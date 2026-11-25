//go:build linux

package local

import (
	"errors"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// isMountRoot lit STATX_ATTR_MOUNT_ROOT sur le handle du dossier : un montage lié du même système de fichiers
// garde le périphérique, seul cet attribut le révèle. Un noyau sans l'attribut (avant 5.8) rend false : seul le
// contrôle du périphérique reste alors, et Capabilities.Reasons["mountRoot"] le signale.
func isMountRoot(dir *os.Root) (bool, error) {
	if !mountRootDetectable() {
		return false, nil
	}
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

// reasonStatxMountRootUnsupported : noyau sans STATX_ATTR_MOUNT_ROOT (avant 5.8) ; un montage lié du même système
// de fichiers sous la racine d'écriture n'est pas détecté (doctor avertit, C16).
const reasonStatxMountRootUnsupported = "statx_mount_root_unsupported"

// mountRootDetectable sonde une fois si le noyau donne STATX_ATTR_MOUNT_ROOT (sur "/", toujours une racine de
// montage) ; variable pour que les tests simulent un noyau ancien.
var mountRootDetectable = sync.OnceValue(probeMountRootDetection)

func probeMountRootDetection() bool {
	var statx unix.Statx_t
	err := unix.Statx(unix.AT_FDCWD, "/", unix.AT_SYMLINK_NOFOLLOW, unix.STATX_TYPE, &statx)
	return err == nil && statx.Attributes_mask&unix.STATX_ATTR_MOUNT_ROOT != 0
}

// recordMountRootGap note dans Reasons l'absence de détection des montages liés : ne pas échouer fermé (Synology,
// RHEL 8 tournent sur des noyaux plus anciens), mais le dire pour que doctor avertisse.
func recordMountRootGap(reasons map[string]string) {
	if !mountRootDetectable() {
		reasons[reasonKeyMountRoot] = reasonStatxMountRootUnsupported
	}
}
