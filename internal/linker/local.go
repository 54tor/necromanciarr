package linker

import "necromanciarr/internal/fsys/local"

// FilesystemKind classe le volume qui porte un chemin ; défini par fsys/local, réexporté ici sans changement.
type FilesystemKind = local.FilesystemKind

// Sortes de volumes ; Unknown compte comme non fiable.
const (
	Unknown = local.Unknown
	Local   = local.Local
	Fuse    = local.Fuse
	Network = local.Network
)

// FilesystemKindOf lit le type du système de fichiers qui porte path (voir local.FilesystemKindOf).
func FilesystemKindOf(path string) (FilesystemKind, error) {
	return local.FilesystemKindOf(path)
}
