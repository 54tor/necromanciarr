//go:build !linux && !windows

package linker

// FilesystemKindOf ne sait pas classer le volume sur cette plateforme : Unknown, donc inode jugé non fiable.
func FilesystemKindOf(_ string) (FilesystemKind, error) {
	return Unknown, nil
}
