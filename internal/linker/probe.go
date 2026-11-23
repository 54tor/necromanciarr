package linker

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Capabilities dit quelles sortes de liens le volume accepte réellement ; Reasons donne l'erreur typée de chaque refus.
// SourceDir et DestDir sont les dossiers résolus réellement sondés.
type Capabilities struct {
	Hardlink  bool
	Symlink   bool
	Reflink   bool
	Reasons   map[Kind]error
	SourceDir string
	DestDir   string
}

const (
	probePrefix        = ".necromanciarr-probe-"
	probeFileMode      = 0o600
	errProbeWrongInode = "linker: probe link does not resolve to the probe file"
)

// Probe sonde directory en y créant puis supprimant des entrées « .necromanciarr-probe-<aléatoire> » ; aucun autre
// fichier n'est touché. Un montage rclone ou FUSE refuse souvent symlink et hardlink : seule une vraie création le dit.
func Probe(directory string) (Capabilities, error) {
	return ProbeBetween(directory, directory)
}

// ProbeBetween sonde la création, dans destDir, de liens vers un fichier d'essai créé dans sourceDir ; hardlink et
// reflink exigent alors le même système de fichiers.
func ProbeBetween(sourceDir, destDir string) (Capabilities, error) {
	return systemApplier().probeBetween(sourceDir, destDir)
}

func systemApplier() applier {
	return applier{linkers: map[Kind]linkFunc{Hardlink: hardlink, Reflink: reflink, Symlink: symlink}}
}

func (linkApplier applier) probeBetween(sourceDir, destDir string) (capabilities Capabilities, err error) {
	resolvedSource, err := resolveProbeDirectory(sourceDir)
	if err != nil {
		return Capabilities{}, err
	}
	resolvedDest, err := resolveProbeDirectory(destDir)
	if err != nil {
		return Capabilities{}, err
	}
	probePath, probeInfo, err := createProbeFile(resolvedSource)
	if err != nil {
		return Capabilities{}, err
	}
	defer func() { err = errors.Join(err, removeProbeEntry(probePath)) }()
	probedKinds := []Kind{Hardlink, Reflink, Symlink}
	capabilities = Capabilities{Reasons: make(map[Kind]error, len(probedKinds)), SourceDir: resolvedSource, DestDir: resolvedDest}
	for _, kind := range probedKinds {
		reason, cleanupErr := linkApplier.probeKind(kind, probePath, probeInfo, resolvedDest)
		if cleanupErr != nil {
			return Capabilities{}, cleanupErr
		}
		capabilities.record(kind, reason)
	}
	return capabilities, nil
}

func (capabilities *Capabilities) record(kind Kind, reason error) {
	if reason != nil {
		capabilities.Reasons[kind] = reason
		return
	}
	switch kind {
	case Hardlink:
		capabilities.Hardlink = true
	case Reflink:
		capabilities.Reflink = true
	case Symlink:
		capabilities.Symlink = true
	}
}

// resolveProbeDirectory : un dossier lui-même symbolique est légitime, les entrées d'essai sont créées dans sa cible.
func resolveProbeDirectory(directory string) (string, error) {
	absoluteDirectory, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	resolvedDirectory, err := filepath.EvalSymlinks(absoluteDirectory)
	if err != nil {
		return "", fmt.Errorf("linker: probe directory %s: %w", directory, err)
	}
	info, err := os.Stat(resolvedDirectory)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("linker: probe directory %s is not a directory", directory)
	}
	return resolvedDirectory, nil
}

func probeEntryPath(directory string) string {
	return filepath.Join(directory, probePrefix+rand.Text())
}

// createProbeFile crée en exclusif : un nom déjà pris, même par un lien pendant, fait échouer au lieu d'être suivi.
func createProbeFile(directory string) (string, fs.FileInfo, error) {
	probePath := probeEntryPath(directory)
	probeFile, err := os.OpenFile(probePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, probeFileMode) // #nosec G304 -- nom aléatoire sous le dossier sondé
	if err != nil {
		return "", nil, fmt.Errorf("linker: create probe file: %w", err)
	}
	info, statErr := probeFile.Stat()
	if closeErr := probeFile.Close(); statErr != nil || closeErr != nil {
		return "", nil, errors.Join(statErr, closeErr, removeProbeEntry(probePath))
	}
	return probePath, info, nil
}

// probeKind renvoie la raison du refus de kind, et séparément l'échec du nettoyage, qui doit arrêter la sonde.
func (linkApplier applier) probeKind(kind Kind, probePath string, probeInfo fs.FileInfo, destDir string) (reason, cleanupErr error) {
	linker, known := linkApplier.linkers[kind]
	if !known {
		return fmt.Errorf("%w: %v", ErrUnsupported, kind), nil
	}
	linkPath := probeEntryPath(destDir)
	source := probePath
	if kind == Symlink {
		relativeSource, err := RelativeSymlinkTarget(linkPath, probePath)
		if err != nil {
			return err, nil
		}
		source = relativeSource
	}
	if err := linker(source, linkPath); err != nil {
		return err, nil
	}
	return checkProbeLink(kind, linkPath, probeInfo), removeProbeEntry(linkPath)
}

func checkProbeLink(kind Kind, linkPath string, probeInfo fs.FileInfo) error {
	linkInfo, err := os.Stat(linkPath)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLinkRefused, err)
	}
	// Un reflink est un fichier distinct : seule son existence compte ; symlink et hardlink doivent mener au fichier d'essai.
	if kind != Reflink && !os.SameFile(probeInfo, linkInfo) {
		return fmt.Errorf("%w: %s", ErrLinkRefused, errProbeWrongInode)
	}
	return nil
}

func removeProbeEntry(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("linker: remove probe entry: %w", err)
	}
	return nil
}
