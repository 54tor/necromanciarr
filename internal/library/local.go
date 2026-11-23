package library

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// Scan parcourt root et enregistre chaque fichier média dans mediaStore.
// Si root est un lien symbolique, la cible est parcourue (WalkDir ne suit pas
// le lien de départ) et les chemins enregistrés restent préfixés par root.
func Scan(ctx context.Context, root string, mediaStore *store.Store) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("scan %s: %w", root, err)
	}
	rootInfo, err := os.Stat(resolvedRoot)
	if err != nil {
		return fmt.Errorf("scan %s: %w", root, err)
	}
	rootDevice, rootDeviceKnown := deviceOf(rootInfo)

	return filepath.WalkDir(resolvedRoot, func(entryPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return skipForeignDevice(entry, rootDevice, rootDeviceKnown)
		}
		if !torrentcheck.IsMediaPath(entryPath) {
			return nil
		}
		return recordFile(ctx, mediaStore, rebasePath(entryPath, resolvedRoot, root), entry)
	})
}

func skipForeignDevice(entry fs.DirEntry, rootDevice uint64, rootDeviceKnown bool) error {
	if !rootDeviceKnown {
		return nil
	}
	info, err := entry.Info()
	if err != nil {
		return err
	}
	entryDevice, known := deviceOf(info)
	if known && entryDevice != rootDevice {
		return filepath.SkipDir
	}
	return nil
}

func recordFile(ctx context.Context, mediaStore *store.Store, entryPath string, entry fs.DirEntry) error {
	info, err := entry.Info()
	if err != nil {
		return err
	}
	return mediaStore.UpsertMediaFile(ctx, store.MediaFile{
		Path:    entryPath,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	})
}

func rebasePath(entryPath, resolvedRoot, root string) string {
	relativePath, err := filepath.Rel(resolvedRoot, entryPath)
	if err != nil {
		return entryPath
	}
	return filepath.Join(root, relativePath)
}
