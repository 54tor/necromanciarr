package fsys

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// sftpScheme préfixe une Location distante ; tout autre texte est un chemin local.
const sftpScheme = "sftp://"

// maxLocationLength borne la taille d'une Location (PATH_MAX courant).
const maxLocationLength = 4096

// Location est un chemin qualifié : Volume vide pour le disque local, nom du fournisseur sftp sinon.
// Deux Location ne se comparent jamais par leur texte brut : seul Registry compare, sur les formes canoniques.
type Location struct{ Volume, Path string }

// ParseLocation lit "sftp://seedbox/srv/media" (chemin distant nettoyé en POSIX, sans "..") ou un chemin local
// absolu, gardé tel quel : sa forme canonique dépend des liens symboliques, que seul le volume connaît.
func ParseLocation(raw string) (Location, error) {
	if err := checkRawLocation(raw); err != nil {
		return Location{}, err
	}
	remainder, isRemote := strings.CutPrefix(raw, sftpScheme)
	if isRemote {
		return parseRemoteLocation(remainder)
	}
	if !filepath.IsAbs(raw) {
		return Location{}, invalidLocation("local path is not absolute")
	}
	return Location{Path: raw}, nil
}

// String rend la forme textuelle relue par ParseLocation.
func (location Location) String() string {
	if location.Volume == "" {
		return location.Path
	}
	return sftpScheme + location.Volume + location.Path
}

func checkRawLocation(raw string) error {
	if raw == "" {
		return invalidLocation("empty location")
	}
	if len(raw) > maxLocationLength {
		return invalidLocation("location too long")
	}
	for _, character := range raw {
		isControl := character < 0x20 || character == 0x7f
		if isControl {
			return invalidLocation("location contains a control character")
		}
	}
	return nil
}

func parseRemoteLocation(remainder string) (Location, error) {
	volume, remotePath, hasPath := strings.Cut(remainder, "/")
	if !isValidVolumeName(volume) {
		return Location{}, invalidLocation("volume name must match [a-z0-9-]+")
	}
	if !hasPath {
		return Location{}, invalidLocation("remote location has no path")
	}
	for _, segment := range strings.Split(remotePath, "/") {
		if segment == ".." {
			return Location{}, invalidLocation("remote path contains ..")
		}
	}
	return Location{Volume: volume, Path: path.Clean("/" + remotePath)}, nil
}

func isValidVolumeName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		isAllowed := (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-'
		if !isAllowed {
			return false
		}
	}
	return true
}

func invalidLocation(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidLocation, reason)
}
