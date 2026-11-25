package fsys

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// sftpScheme préfixe une Location distante ; tout autre texte est un chemin local.
const sftpScheme = "sftp://"

// maxLocationLength borne la taille d'une Location (PATH_MAX courant).
const maxLocationLength = 4096

// Location est un chemin qualifié : Volume vide pour le disque local, nom du fournisseur sftp sinon.
// Deux Location ne se comparent jamais par leur texte brut : seul Registry compare, sur les formes canoniques.
type Location struct{ Volume, Path string }

// ParseLocation lit "sftp://seedbox/srv/media" (nettoyé par path.Clean) ou un chemin local absolu (nettoyé par
// filepath.Clean) ; tout segment ".." est refusé avant nettoyage, car le résoudre lexicalement à travers un lien
// symbolique changerait le dossier désigné. La forme canonique reste l'affaire du volume (Canonical).
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
	if slices.Contains(strings.FieldsFunc(raw, isLocalSeparator), "..") {
		return Location{}, invalidLocation("local path contains ..")
	}
	return Location{Path: filepath.Clean(raw)}, nil
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
	if !utf8.ValidString(raw) {
		return invalidLocation("location is not valid UTF-8")
	}
	for _, character := range raw {
		// C0, DEL, C1 et contrôles bidirectionnels : invisibles ou trompeurs à l'affichage d'un chemin.
		isRefused := unicode.IsControl(character) || unicode.Is(unicode.Bidi_Control, character)
		if isRefused {
			return invalidLocation("location contains a control character")
		}
	}
	return nil
}

func isLocalSeparator(character rune) bool {
	return character < utf8.RuneSelf && os.IsPathSeparator(uint8(character))
}

func parseRemoteLocation(remainder string) (Location, error) {
	volume, remotePath, hasPath := strings.Cut(remainder, "/")
	if !isValidVolumeName(volume) {
		return Location{}, invalidLocation("volume name must match [a-z0-9-]+")
	}
	if !hasPath {
		return Location{}, invalidLocation("remote location has no path")
	}
	if slices.Contains(strings.Split(remotePath, "/"), "..") {
		return Location{}, invalidLocation("remote path contains ..")
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
