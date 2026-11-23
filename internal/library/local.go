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

func Scan(ctx context.Context, root string, mediaStore *store.Store) error {
	rootInfo, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("scan %s: %w", root, err)
	}
	rootDevice, rootDeviceKnown := deviceOf(rootInfo)

	return filepath.WalkDir(root, func(entryPath string, entry fs.DirEntry, walkErr error) error {
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
		return recordFile(ctx, mediaStore, entryPath, entry)
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
