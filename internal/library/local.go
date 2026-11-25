// Package library parcourt une bibliothèque et en inventorie les fichiers média, sur un volume local ou distant.
package library

import (
	"context"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// walker porte l'état d'un parcours : le volume lu, son identité de départ et la racine d'origine des chemins.
type walker struct {
	ctx        context.Context
	readFS     fsys.ReadFS
	mediaStore *store.Store
	volume     string
	rootVolume string
	rootKnown  bool
}

// Scan parcourt root et enregistre chaque fichier média dans mediaStore. Si root est un lien symbolique, la cible
// est parcourue et les chemins enregistrés restent préfixés par root (sous la forme Location.String : chemin brut
// en local, sftp://<fournisseur>/… à distance). Les liens symboliques et les dossiers d'un autre volume (montage
// imbriqué) ne sont ni suivis ni parcourus ; l'ordre est celui de ReadDir, trié par nom.
func Scan(ctx context.Context, root fsys.Location, registry *fsys.Registry, mediaStore *store.Store) error {
	readFS, rootPath, err := registry.Read(root)
	if err != nil {
		return fmt.Errorf("scan %s: %w", root, err)
	}
	resolvedRoot, err := readFS.Canonical(rootPath)
	if err != nil {
		return fmt.Errorf("scan %s: %w", root, err)
	}
	rootInfo, err := readFS.Stat(resolvedRoot)
	if err != nil {
		return fmt.Errorf("scan %s: %w", root, err)
	}
	rootVolume, rootKnown, volumeErr := readFS.VolumeID(resolvedRoot)
	walk := walker{
		ctx: ctx, readFS: readFS, mediaStore: mediaStore, volume: root.Volume,
		rootVolume: rootVolume, rootKnown: rootKnown && volumeErr == nil,
	}
	if !rootInfo.IsDir() {
		return walk.visitFile(fs.FileInfoToDirEntry(rootInfo), resolvedRoot, rootPath)
	}
	return walk.walkDir(resolvedRoot, rootPath)
}

// walkDir liste realDir (chemin résolu) et rend les chemins sous la forme displayDir (chemin d'origine de la racine).
func (walk walker) walkDir(realDir, displayDir string) error {
	if err := walk.ctx.Err(); err != nil {
		return err
	}
	entries, err := walk.readFS.ReadDir(realDir)
	if err != nil {
		return err
	}
	slices.SortFunc(entries, func(first, second fs.DirEntry) int { return strings.Compare(first.Name(), second.Name()) })
	syntax := walk.readFS.Paths()
	for _, entry := range entries {
		if err := walk.ctx.Err(); err != nil {
			return err
		}
		realPath, displayPath := syntax.Join(realDir, entry.Name()), syntax.Join(displayDir, entry.Name())
		if err := walk.visit(entry, realPath, displayPath); err != nil {
			return err
		}
	}
	return nil
}

func (walk walker) visit(entry fs.DirEntry, realPath, displayPath string) error {
	// Les liens symboliques ne sont pas suivis : évite les boucles et la sortie de la bibliothèque.
	if entry.Type()&fs.ModeSymlink != 0 {
		return nil
	}
	if !entry.IsDir() {
		return walk.visitFile(entry, realPath, displayPath)
	}
	foreign, err := walk.isForeignVolume(realPath)
	if err != nil || foreign {
		return err
	}
	return walk.walkDir(realPath, displayPath)
}

// isForeignVolume dit si le dossier est sur un autre volume que la racine (montage imbriqué), hors de la bibliothèque.
func (walk walker) isForeignVolume(dir string) (bool, error) {
	if !walk.rootKnown {
		return false, nil
	}
	dirVolume, known, err := walk.readFS.VolumeID(dir)
	if err != nil {
		return false, err
	}
	return known && dirVolume != walk.rootVolume, nil
}

func (walk walker) visitFile(entry fs.DirEntry, realPath, displayPath string) error {
	if !torrentcheck.IsMediaPath(realPath) {
		return nil
	}
	info, err := entry.Info()
	if err != nil {
		return err
	}
	storedPath := fsys.Location{Volume: walk.volume, Path: displayPath}.String()
	return walk.mediaStore.UpsertMediaFile(walk.ctx, store.MediaFile{
		Path:    storedPath,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	})
}
