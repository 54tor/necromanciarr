package linker

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"necromanciarr/internal/fsys"
)

// Capabilities dit quelles sortes de liens le volume accepte réellement ; Reasons donne l'erreur typée de chaque refus.
// RenameOverwrite dit qu'un renommage par-dessus un autre fichier y aboutit (RenameReason sinon) : la sonde ne voit
// que l'état final, pas l'atomicité. SourceDir et DestDir sont les dossiers résolus réellement sondés.
type Capabilities struct {
	Hardlink        bool
	Symlink         bool
	Reflink         bool
	RenameOverwrite bool
	Reasons         map[Kind]error
	RenameReason    error
	SourceDir       string
	DestDir         string
}

// ErrRenameOverwrite signale un volume où renommer un fichier par-dessus un autre échoue ou laisse un état incohérent.
var ErrRenameOverwrite = errors.New("linker: rename over an existing file failed")

const (
	probePrefix        = ".necromanciarr-probe-"
	probeFileMode      = 0o600
	probeRenameContent = "necromanciarr-probe-rename"
	errProbeWrongInode = "linker: probe link does not resolve to the probe file"
)

// Probe sonde root en y créant puis supprimant des entrées « .necromanciarr-probe-<aléatoire> » ; aucun autre
// fichier n'est touché, rien n'est écrit hors de root. Un montage rclone ou FUSE refuse souvent symlink et hardlink :
// seule une vraie création le dit.
func Probe(root fsys.WriteRoot) (Capabilities, error) {
	return systemApplier().probe(root)
}

// ProbeDirectory ouvre directory par registry (refus d'un dossier qui recouvre la bibliothèque) puis le sonde
// comme Probe.
func ProbeDirectory(registry *fsys.Registry, directory fsys.Location) (Capabilities, error) {
	root, err := registry.WriteRoot(directory)
	if err != nil {
		return Capabilities{}, err
	}
	defer func() { _ = root.Close() }()
	return Probe(root)
}

// probeSubject est le fichier que visent les liens d'essai ; view sert à suivre un lien et à comparer les
// identités (nil : seul le Lstat de la racine est disponible).
type probeSubject struct {
	location      fsys.Location
	info          fs.FileInfo
	symlinkTarget string
	view          fsys.ReadFS
}

func (linkApplier applier) probe(root fsys.WriteRoot) (capabilities Capabilities, err error) {
	probeName, probeInfo, err := createProbeFile(root, "")
	if err != nil {
		return Capabilities{}, err
	}
	defer func() { err = errors.Join(err, removeProbeEntry(root, probeName)) }()
	subject := probeSubject{
		location:      fsys.Location{Volume: root.Root().Volume, Path: absoluteIn(root, probeName)},
		info:          probeInfo,
		symlinkTarget: probeName,
		view:          readViewOf(root),
	}
	rootPath := root.Root().Path
	capabilities = Capabilities{Reasons: map[Kind]error{}, SourceDir: rootPath, DestDir: rootPath}
	for _, kind := range []Kind{Hardlink, Reflink, Symlink} {
		if err := linkApplier.probeAndRecord(&capabilities, kind, root, subject); err != nil {
			return Capabilities{}, err
		}
	}
	capabilities.RenameReason, err = linkApplier.probeRenameOverwrite(root, subject.view)
	capabilities.RenameOverwrite = capabilities.RenameReason == nil
	return capabilities, err
}

// probeAndRecord note la raison d'un refus ; seul l'échec du nettoyage arrête la sonde.
func (linkApplier applier) probeAndRecord(capabilities *Capabilities, kind Kind, root fsys.WriteRoot, subject probeSubject) error {
	reason, cleanupErr := linkApplier.probeKind(kind, root, subject)
	if cleanupErr != nil {
		return cleanupErr
	}
	capabilities.record(kind, reason)
	return nil
}

// probeRenameOverwrite renomme un fichier d'essai A par-dessus un fichier d'essai B, puis exige que B porte le contenu
// de A et que A ait disparu ; la raison du refus et l'échec du nettoyage sont renvoyés séparément.
func (linkApplier applier) probeRenameOverwrite(root fsys.WriteRoot, view fsys.ReadFS) (reason, cleanupErr error) {
	rename := linkApplier.rename
	if rename == nil {
		rename = func(root fsys.WriteRoot, oldRelative, newRelative string) error {
			return root.Replace(oldRelative, newRelative)
		}
	}
	movedName, _, err := createProbeFile(root, probeRenameContent)
	if err != nil {
		return nil, err
	}
	defer func() { cleanupErr = errors.Join(cleanupErr, removeProbeEntry(root, movedName)) }()
	replacedName, _, err := createProbeFile(root, "")
	if err != nil {
		return nil, err
	}
	defer func() { cleanupErr = errors.Join(cleanupErr, removeProbeEntry(root, replacedName)) }()
	if err := rename(root, movedName, replacedName); err != nil {
		return fmt.Errorf("%w: %w", ErrRenameOverwrite, err), nil
	}
	return checkRenamed(root, view, movedName, replacedName), nil
}

func checkRenamed(root fsys.WriteRoot, view fsys.ReadFS, movedName, replacedName string) error {
	if !holdsContent(root, view, replacedName, probeRenameContent) {
		return fmt.Errorf("%w: destination does not hold the renamed content", ErrRenameOverwrite)
	}
	if _, err := root.Lstat(movedName); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: source still present after rename", ErrRenameOverwrite)
	}
	return nil
}

// holdsContent relit le fichier quand la vue du volume est connue ; sinon sa taille seule est comparée.
func holdsContent(root fsys.WriteRoot, view fsys.ReadFS, relative, content string) bool {
	info, err := root.Lstat(relative)
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(content)) {
		return false
	}
	if view == nil {
		return true
	}
	existing, err := readWhole(view, absoluteIn(root, relative), info.Size())
	return err == nil && bytes.Equal(existing, []byte(content))
}

// readWhole lit au plus size+1 octets : un fichier qui a grandi depuis le Lstat ne passe pas pour identique.
func readWhole(view fsys.ReadFS, name string, size int64) ([]byte, error) {
	file, err := view.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.NewSectionReader(file, 0, size+1))
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

func probeEntryName() string {
	return probePrefix + rand.Text()
}

// createProbeFile crée en exclusif : un nom déjà pris, même par un lien pendant, fait échouer au lieu d'être suivi.
func createProbeFile(root fsys.WriteRoot, content string) (string, fs.FileInfo, error) {
	probeName := probeEntryName()
	writer, err := root.CreateExclusive(probeName, probeFileMode)
	if err != nil {
		return "", nil, fmt.Errorf("linker: create probe file: %w", confined(err))
	}
	_, writeErr := io.WriteString(writer, content)
	closeErr := writer.Close()
	info, statErr := root.Lstat(probeName)
	if writeErr != nil || closeErr != nil || statErr != nil {
		return "", nil, errors.Join(writeErr, closeErr, statErr, removeProbeEntry(root, probeName))
	}
	return probeName, info, nil
}

// probeKind renvoie la raison du refus de kind, et séparément l'échec du nettoyage, qui doit arrêter la sonde.
func (linkApplier applier) probeKind(kind Kind, root fsys.WriteRoot, subject probeSubject) (reason, cleanupErr error) {
	linker, known := linkApplier.linkers[kind]
	if !known {
		return fmt.Errorf("%w: %v", ErrUnsupported, kind), nil
	}
	linkName := probeEntryName()
	source := subject.location
	if kind == Symlink {
		source = fsys.Location{Volume: source.Volume, Path: subject.symlinkTarget}
	}
	if err := linker(root, source, linkName); err != nil {
		return confined(err), nil
	}
	return checkProbeLink(kind, root, subject, linkName), removeProbeEntry(root, linkName)
}

func checkProbeLink(kind Kind, root fsys.WriteRoot, subject probeSubject, linkName string) error {
	linkInfo, err := root.Lstat(linkName)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLinkRefused, err)
	}
	// Un reflink est un fichier distinct : seule son existence compte ; symlink et hardlink doivent mener au fichier d'essai.
	switch {
	case kind == Reflink:
		return nil
	case kind == Symlink && subject.view == nil:
		return checkIsSymlink(linkInfo)
	case kind == Symlink:
		linkInfo, err = subject.view.Stat(absoluteIn(root, linkName))
		if err != nil {
			return fmt.Errorf("%w: %w", ErrLinkRefused, err)
		}
	}
	if !leadsToSubject(subject, linkInfo) {
		return fmt.Errorf("%w: %s", ErrLinkRefused, errProbeWrongInode)
	}
	return nil
}

func checkIsSymlink(linkInfo fs.FileInfo) error {
	if linkInfo.Mode()&fs.ModeSymlink == 0 {
		return fmt.Errorf("%w: probe entry is not a symbolic link", ErrLinkRefused)
	}
	return nil
}

// leadsToSubject : sans vue du volume, un lien dur se juge à son type et à sa taille.
func leadsToSubject(subject probeSubject, linkInfo fs.FileInfo) bool {
	if subject.view == nil {
		return linkInfo.Mode().IsRegular() && linkInfo.Size() == subject.info.Size()
	}
	return sameFile(subject.view, subject.info, linkInfo)
}

func removeProbeEntry(root fsys.WriteRoot, relative string) error {
	if err := removeIfPresent(root, relative); err != nil {
		return fmt.Errorf("linker: remove probe entry: %w", err)
	}
	return nil
}
