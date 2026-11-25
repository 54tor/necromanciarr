package local

// Clés et codes de Capabilities.Reasons (contrat machine, jamais traduits).
const (
	reasonKeyFileIdentity = "fileIdentity"
	reasonKeyReflink      = "reflink"
	reasonKeySymlink      = "symlink"
	reasonKeyServerCopy   = "serverCopy"
	reasonKeyMountRoot    = "mountRoot"

	reasonLocalVolume             = "local_volume"
	reasonUnsupportedPlatform     = "unsupported_platform"
	reasonNoFileIndex             = "no_file_index"
	reasonSymlinkPrivilegeMissing = "symlink_privilege_missing"
)
