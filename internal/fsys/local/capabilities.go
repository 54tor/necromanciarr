package local

// Clés et codes de Capabilities.Reasons (contrat machine, jamais traduits).
const (
	reasonKeyFileIdentity = "fileIdentity"
	reasonKeyReflink      = "reflink"
	reasonKeySymlink      = "symlink"
	reasonKeyServerCopy   = "serverCopy"
	reasonKeyMountRoot    = ReasonKeyMountRoot

	reasonLocalVolume             = "local_volume"
	reasonUnsupportedPlatform     = "unsupported_platform"
	reasonNoFileIndex             = "no_file_index"
	reasonSymlinkPrivilegeMissing = "symlink_privilege_missing"
)

// ReasonKeyMountRoot et ReasonMountRootUnsupported : sous Linux avant 5.8 (sans STATX_ATTR_MOUNT_ROOT),
// Capabilities.Reasons[ReasonKeyMountRoot] vaut ReasonMountRootUnsupported ; un montage lié du même système de
// fichiers sous une racine d'écriture n'est alors pas détecté (doctor : mount_root_undetected).
const (
	ReasonKeyMountRoot         = "mountRoot"
	ReasonMountRootUnsupported = "statx_mount_root_unsupported"
)
