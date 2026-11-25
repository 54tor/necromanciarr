package dedupe

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/linker"
)

// remotePrefix repère la forme enregistrée d'un chemin distant (sftp://<fournisseur>/<chemin>).
const remotePrefix = "sftp://"

// errNotBelow : le chemin n'est pas sous la racine donnée (autre volume, « .. », racine elle-même).
var errNotBelow = errors.New("dedupe: path is not below its root")

// Files lit et situe les chemins enregistrés (bruts en local, sftp://… à distance) à travers un registre : aucun
// accès disque de dedupe ni de repair n'en sort. Un registre nil lit le seul disque local et refuse toute écriture.
type Files struct {
	registry *fsys.Registry
}

// NewFiles rend l'accès aux fichiers par registry (nil : disque local en lecture seule).
func NewFiles(registry *fsys.Registry) Files {
	return Files{registry: registry}
}

// localReadRegistry : sans racine de bibliothèque, ce registre ne sert qu'à lire (WriteRoot refusé).
var localReadRegistry = sync.OnceValue(func() *fsys.Registry {
	registry, err := local.NewRegistry()
	if err != nil {
		panic(err) // un registre à un seul volume local ne peut pas être refusé
	}
	return registry
})

func (files Files) readRegistry() *fsys.Registry {
	if files.registry == nil {
		return localReadRegistry()
	}
	return files.registry
}

// IsRemote dit si stored est la forme enregistrée d'un chemin distant.
func IsRemote(stored string) bool {
	return strings.HasPrefix(stored, remotePrefix)
}

func (files Files) resolve(stored string) (fsys.ReadFS, fsys.Location, error) {
	location, err := fsys.StoredLocation(stored)
	if err != nil {
		return nil, fsys.Location{}, err
	}
	view, name, err := files.readRegistry().Read(location)
	if err != nil {
		return nil, fsys.Location{}, err
	}
	return view, fsys.Location{Volume: location.Volume, Path: name}, nil
}

// Lstat lit l'information de stored sans suivre un lien symbolique final.
func (files Files) Lstat(stored string) (fs.FileInfo, error) {
	view, location, err := files.resolve(stored)
	if err != nil {
		return nil, err
	}
	return view.Lstat(location.Path)
}

// Stat lit l'information de stored en suivant les liens symboliques.
func (files Files) Stat(stored string) (fs.FileInfo, error) {
	view, location, err := files.resolve(stored)
	if err != nil {
		return nil, err
	}
	return view.Stat(location.Path)
}

// Readlink rend le texte du lien symbolique stored.
func (files Files) Readlink(stored string) (string, error) {
	view, location, err := files.resolve(stored)
	if err != nil {
		return "", err
	}
	return view.Readlink(location.Path)
}

// Open ouvre stored en lecture seule.
func (files Files) Open(stored string) (fsys.File, error) {
	view, location, err := files.resolve(stored)
	if err != nil {
		return nil, err
	}
	return view.Open(location.Path)
}

// Canonical rend la forme enregistrée du chemin canonique de stored (liens résolus par le volume).
func (files Files) Canonical(stored string) (string, error) {
	view, location, err := files.resolve(stored)
	if err != nil {
		return "", err
	}
	canonical, err := view.Canonical(location.Path)
	if err != nil {
		return "", err
	}
	return fsys.Location{Volume: location.Volume, Path: canonical}.String(), nil
}

// Capabilities rend les capacités du volume de stored.
func (files Files) Capabilities(ctx context.Context, stored string) (fsys.Capabilities, error) {
	view, _, err := files.resolve(stored)
	if err != nil {
		return fsys.Capabilities{}, err
	}
	return view.Capabilities(ctx)
}

// Walk visite, en ordre lexical et en profondeur, chaque entrée sous root (root exclue), sans suivre un dossier
// symbolique ; ctx est contrôlé à chaque entrée.
func (files Files) Walk(ctx context.Context, root string, visit func(stored string, entry fs.DirEntry) error) error {
	view, location, err := files.resolve(root)
	if err != nil {
		return err
	}
	entries, err := view.ReadDir(location.Path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		child := Join(root, entry.Name())
		if err := visit(child, entry); err != nil {
			return err
		}
		if !entry.IsDir() {
			continue
		}
		if err := files.Walk(ctx, child, visit); err != nil {
			return err
		}
	}
	return nil
}

// FileID rend l'identité de info au sens du volume de stored (contrôles qui ne détruisent rien).
func (files Files) FileID(stored string, info fs.FileInfo) (fsys.FileID, bool) {
	view, _, err := files.resolve(stored)
	if err != nil {
		return fsys.FileID{}, false
	}
	return view.FileID(info)
}

// SameFile dit que les deux informations désignent le même fichier : identités connues, égales, même volume. Une
// identité inconnue n'est jamais « le même fichier ».
func (files Files) SameFile(firstStored string, first fs.FileInfo, secondStored string, second fs.FileInfo) bool {
	if volumeOf(firstStored) != volumeOf(secondStored) {
		return false
	}
	firstID, firstKnown := files.FileID(firstStored, first)
	secondID, secondKnown := files.FileID(secondStored, second)
	return firstKnown && secondKnown && firstID == secondID
}

// ReliableID est l'identité stricte qu'exige une décision destructive : local.ReliableFileID sur le disque local,
// jamais à distance (une identité SFTP n'est jamais une preuve, I4).
func ReliableID(stored string, info fs.FileInfo) (fsys.FileID, bool) {
	if IsRemote(stored) {
		return fsys.FileID{}, false
	}
	return local.ReliableFileID(info)
}

// WriteRoot ouvre, par le registre, la racine d'écriture du dossier stored ; registre nil : fsys.ErrNoLibraryRoots.
func (files Files) WriteRoot(stored string) (fsys.WriteRoot, error) {
	registry, err := writeRegistry(files.registry)
	if err != nil {
		return nil, err
	}
	location, err := fsys.StoredLocation(stored)
	if err != nil {
		return nil, err
	}
	return registry.WriteRoot(location)
}

// SymlinkText rend le texte d'un lien symbolique posé à linkStored vers targetStored : relatif (dossiers résolus
// par le volume) ou chemin complet du volume ; jamais un repli silencieux de l'un vers l'autre.
func (files Files) SymlinkText(linkStored, targetStored string, relative bool) (string, error) {
	if !IsRemote(linkStored) && !IsRemote(targetStored) {
		if relative {
			return linker.RelativeSymlinkTarget(linkStored, targetStored)
		}
		return filepath.Abs(targetStored)
	}
	if volumeOf(linkStored) != volumeOf(targetStored) {
		return "", fmt.Errorf("%w: link and target on two volumes", linker.ErrNoRelativePath)
	}
	if !relative {
		return VolumePath(targetStored), nil
	}
	linkDir, err := files.Canonical(Dir(linkStored))
	if err != nil {
		return "", err
	}
	targetDir, err := files.Canonical(Dir(targetStored))
	if err != nil {
		return "", err
	}
	relativeDir, err := fsys.RemotePaths().Rel(VolumePath(linkDir), VolumePath(targetDir))
	if err != nil {
		return "", fmt.Errorf("%w: %w", linker.ErrNoRelativePath, err)
	}
	return path.Join(relativeDir, Base(targetStored)), nil
}

// VolumeOf rend le volume d'un chemin enregistré ("" en local, ou pour une adresse illisible).
func VolumeOf(stored string) string {
	return volumeOf(stored)
}

func volumeOf(stored string) string {
	location, err := fsys.StoredLocation(stored)
	if err != nil {
		return ""
	}
	return location.Volume
}

// VolumePath rend le chemin de stored dans la syntaxe de son volume (sans le préfixe sftp://<fournisseur>).
func VolumePath(stored string) string {
	location, err := fsys.StoredLocation(stored)
	if err != nil {
		return stored
	}
	return location.Path
}

// Join joint des segments à un chemin enregistré, dans la syntaxe de son volume.
func Join(stored string, elements ...string) string {
	location, err := fsys.StoredLocation(stored)
	if err != nil || location.Volume == "" {
		return filepath.Join(append([]string{stored}, elements...)...)
	}
	location.Path = path.Join(append([]string{location.Path}, elements...)...)
	return location.String()
}

// Dir rend le dossier d'un chemin enregistré, dans la syntaxe de son volume.
func Dir(stored string) string {
	location, err := fsys.StoredLocation(stored)
	if err != nil || location.Volume == "" {
		return filepath.Dir(stored)
	}
	location.Path = path.Dir(location.Path)
	return location.String()
}

// Base rend le dernier segment d'un chemin enregistré.
func Base(stored string) string {
	if IsRemote(stored) {
		return path.Base(VolumePath(stored))
	}
	return filepath.Base(stored)
}

// Below rend target relatif à root (même volume), en « / », pour une WriteRoot ; refus hors de root, sur un autre
// volume ou pour root elle-même. Un chemin local est d'abord rendu absolu.
func Below(root, target string) (string, error) {
	rootLocation, err := fsys.StoredLocation(root)
	if err != nil {
		return "", err
	}
	targetLocation, err := fsys.StoredLocation(target)
	if err != nil {
		return "", err
	}
	if rootLocation.Volume != targetLocation.Volume {
		return "", errNotBelow
	}
	if rootLocation.Volume == "" {
		return localBelow(rootLocation.Path, targetLocation.Path)
	}
	relative, err := fsys.RemotePaths().Rel(rootLocation.Path, targetLocation.Path)
	isEscape := err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, "../")
	if isEscape {
		return "", errNotBelow
	}
	return relative, nil
}

func localBelow(root, target string) (string, error) {
	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, absoluteTarget)
	if err != nil || relative == "." || !filepath.IsLocal(relative) {
		return "", errNotBelow
	}
	return filepath.ToSlash(relative), nil
}
