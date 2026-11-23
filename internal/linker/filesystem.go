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

// Nombres magiques statfs (linux/magic.h et sources des pilotes) ; un montage rclone apparaît en FUSE.
const fuseSuperMagic uint32 = 0x65735546

// networkMagics regroupe les systèmes de fichiers réseau ou distribués : NFS, SMB/CIFS, 9p, Ceph, AFS, Lustre, GFS2,
// OCFS2, Coda, NCP et OrangeFS.
var networkMagics = map[uint32]bool{
	0x6969:     true,
	0x517b:     true,
	0xff534d42: true,
	0xfe534d42: true,
	0x01021997: true,
	0x00c36400: true,
	0x5346414f: true,
	0x6b414653: true,
	0x0bd00bd0: true,
	0x01161970: true,
	0x7461636f: true,
	0x73757245: true,
	0x564c:     true,
	0x20030528: true,
}

// kindFromMagic : tout ce qui n'est ni FUSE ni réseau est un système de fichiers local (ext4, xfs, btrfs, tmpfs...).
func kindFromMagic(magic uint32) FilesystemKind {
	switch {
	case magic == fuseSuperMagic:
		return Fuse
	case networkMagics[magic]:
		return Network
	default:
		return Local
	}
}
