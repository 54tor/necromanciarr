package local

import (
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"

	"necromanciarr/internal/fsys"
)

// volume est le disque local : nom de volume vide, chemins filepath.
type volume struct{}

// New rend le volume du disque local (nom ""). Il est sans état : plusieurs appels rendent des volumes équivalents.
func New() fsys.Volume { return volume{} }

// Volume rend le nom du volume local : vide.
func (volume) Volume() string { return "" }

// Paths rend la syntaxe filepath de la plateforme.
func (volume) Paths() fsys.PathSyntax { return fsys.LocalPaths() }

// Capabilities rend les capacités de la plateforme ; elles ne dépendent d'aucun dossier, la sonde par dossier
// (linker.Probe) restant la vérité d'un dossier de liens précis.
func (volume) Capabilities(ctx context.Context) (fsys.Capabilities, error) {
	if err := ctx.Err(); err != nil {
		return fsys.Capabilities{}, err
	}
	capabilities := platformCapabilities()
	capabilities.Reasons = maps.Clone(capabilities.Reasons)
	return capabilities, nil
}

// VolumeID rend la clé du système de fichiers qui porte name (périphérique sous Unix, lecteur sous Windows).
func (volume) VolumeID(name string) (string, bool, error) {
	id, err := DeviceKey(name)
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// FreeSpace rend la place disponible pour le compte courant sur le volume de name.
func (volume) FreeSpace(name string) (uint64, error) { return FreeBytes(name) }

// FileID rend périphérique et inode de toute information portant un Stat_t à inode non nul (Unix) : identité de
// dossier pour le contrôle de recouvrement du registre, où une collision fausse (FUSE, réseau) ne cause qu'un
// refus. Un inode nul ou Windows rendent known=false. Pour une décision destructive, utiliser ReliableFileID.
func (volume) FileID(info fs.FileInfo) (fsys.FileID, bool) {
	if tagged, isTagged := info.(localInfo); isTagged {
		info = tagged.FileInfo
	}
	return fileIdentity(info)
}

// ReliableFileID est l'identité stricte : seulement pour une information rendue par ce volume (Stat, Lstat,
// File.Stat, WriteRoot.Lstat) sur un système de fichiers Local au sens de FilesystemKindOf. Réseau, FUSE,
// Windows ou information venue d'ailleurs rendent known=false : c'est celle qu'exige un « même fichier »
// destructif (dedupe, preuve d'un lien).
func ReliableFileID(info fs.FileInfo) (fsys.FileID, bool) {
	tagged, isTagged := info.(localInfo)
	if !isTagged || !tagged.identityReliable {
		return fsys.FileID{}, false
	}
	return fileIdentity(tagged.FileInfo)
}

// Canonical rend la forme sur disque de name : liens résolus puis chemin absolu, sans repli de casse.
func (volume) Canonical(name string) (string, error) {
	resolved, err := filepath.EvalSymlinks(name)
	if err != nil {
		return "", fmt.Errorf("%w: %w", fsys.ErrCannotCanonicalize, err)
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("%w: %w", fsys.ErrCannotCanonicalize, err)
	}
	return absolute, nil
}

// OpenWriteRoot ouvre dir, déjà canonique (Registry.WriteRoot), et compare l'identité du dossier ouvert à un Lstat
// fait juste avant : un dossier remplacé entre Canonical et l'ouverture est refusé.
func (volume) OpenWriteRoot(dir string) (fsys.WriteRoot, error) {
	return openWriteRoot(dir, platformAtomicReplace)
}

func openWriteRoot(dir string, atomicReplace bool) (*writeRoot, error) {
	isCanonicalForm := filepath.IsAbs(dir) && filepath.Clean(dir) == dir
	if !isCanonicalForm {
		return nil, unconfined("write root is not canonical")
	}
	before, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !isPlainDirectory(before) {
		return nil, unconfined("write root is not a plain directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err == nil && !os.SameFile(before, opened) {
		err = unconfined("write root changed while opening")
	}
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	kind, kindErr := FilesystemKindOf(dir)
	return &writeRoot{
		dir: dir, root: root, identity: opened,
		identityReliable: kindErr == nil && kind == Local, atomicReplace: atomicReplace,
	}, nil
}

// unconfined rend ErrUnconfined avec un motif, sans chemin.
func unconfined(reason string) error {
	return fmt.Errorf("%w: %s", fsys.ErrUnconfined, reason)
}
