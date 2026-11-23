package linker

// FilesystemKind classe le volume qui porte un chemin : seuls les volumes locaux ont des inodes stables.
type FilesystemKind int

// Sortes de volumes ; Unknown compte comme non fiable.
const (
	Unknown FilesystemKind = iota
	Local
	Fuse
	Network
)

// String nomme la sorte de volume.
func (kind FilesystemKind) String() string {
	switch kind {
	case Local:
		return "local"
	case Fuse:
		return "fuse"
	case Network:
		return "network"
	default:
		return "unknown"
	}
}

// Nombres magiques statfs (linux/magic.h) ; un montage rclone apparaît en FUSE.
const (
	fuseSuperMagic uint32 = 0x65735546
	nfsSuperMagic  uint32 = 0x6969
	smbSuperMagic  uint32 = 0x517b
	cifsSuperMagic uint32 = 0xff534d42
	smb2SuperMagic uint32 = 0xfe534d42
)

// kindFromMagic : tout ce qui n'est ni FUSE ni réseau est un système de fichiers local (ext4, xfs, btrfs, tmpfs...).
func kindFromMagic(magic uint32) FilesystemKind {
	switch magic {
	case fuseSuperMagic:
		return Fuse
	case nfsSuperMagic, smbSuperMagic, cifsSuperMagic, smb2SuperMagic:
		return Network
	default:
		return Local
	}
}
