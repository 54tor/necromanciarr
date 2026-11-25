package i18n

// Clés des contrôles doctor des fournisseurs SFTP (sftp:<nom>, sftp-library:<nom>, sftp-exec:<nom>) et de leurs extensions.
const (
	KeyDoctorTitleSFTP        Key = "doctor.title.sftp"
	KeyDoctorTitleSFTPLibrary Key = "doctor.title.sftp_library"
	KeyDoctorTitleSFTPExec    Key = "doctor.title.sftp_exec"

	KeyDoctorSFTPTarget             Key = "doctor.sftp.target"
	KeyDoctorSFTPOK                 Key = "doctor.sftp.ok"
	KeyDoctorSFTPHintKnownHosts     Key = "doctor.sftp.hint.known_hosts"
	KeyDoctorSFTPHintHostKeyChanged Key = "doctor.sftp.hint.host_key_changed"
	KeyDoctorSFTPHintAuth           Key = "doctor.sftp.hint.auth"
	KeyDoctorSFTPHintIdentity       Key = "doctor.sftp.hint.identity"
	KeyDoctorSFTPHintUnavailable    Key = "doctor.sftp.hint.unavailable"
	KeyDoctorSFTPHintWindows        Key = "doctor.sftp.hint.windows"
	KeyDoctorSFTPHintChmod          Key = "doctor.sftp.hint.chmod"
	KeyDoctorSFTPHintAgent          Key = "doctor.sftp.hint.agent"
	KeyDoctorSFTPHintHostile        Key = "doctor.sftp.hint.hostile"

	KeyDoctorSFTPLibraryNone                Key = "doctor.sftp_library.none"
	KeyDoctorSFTPLibraryRoot                Key = "doctor.sftp_library.root"
	KeyDoctorSFTPLibraryOK                  Key = "doctor.sftp_library.ok"
	KeyDoctorSFTPLibraryHintNoMedia         Key = "doctor.sftp_library.hint.no_media"
	KeyDoctorSFTPLibraryHintUnread          Key = "doctor.sftp_library.hint.unreadable"
	KeyDoctorSFTPLibraryHintReadOnly        Key = "doctor.sftp_library.hint.read_only_account"
	KeyDoctorSFTPExecOK                     Key = "doctor.sftp_exec.ok"
	KeyDoctorSFTPExecOff                    Key = "doctor.sftp_exec.off"
	KeyDoctorSFTPExecLowPriority            Key = "doctor.sftp_exec.low_priority"
	KeyDoctorSFTPExecNoLowPriority          Key = "doctor.sftp_exec.no_low_priority"
	KeyDoctorSFTPExecNoLinkDir              Key = "doctor.sftp_exec.no_link_dir"
	KeyDoctorSFTPExecHintUnavailable        Key = "doctor.sftp_exec.hint.unavailable"
	KeyDoctorSFTPExecHintRequired           Key = "doctor.sftp_exec.hint.required"
	KeyDoctorVolumeIdentityUnknown          Key = "doctor.links.identity_unknown"
	KeyDoctorVolumeIdentityUnknownHint      Key = "doctor.links.identity_unknown.hint"
	KeyDoctorVolumeAliasLoopback            Key = "doctor.links.volume_alias_loopback"
	KeyDoctorVolumeAliasLoopbackHint        Key = "doctor.links.volume_alias_loopback.hint"
	KeyDoctorVolumeAliasShared              Key = "doctor.links.volume_alias_shared"
	KeyDoctorMountRootUndetected            Key = "doctor.links.mount_root_undetected"
	KeyDoctorMountRootUndetectedHint        Key = "doctor.links.mount_root_undetected.hint"
	KeyDoctorPathRuleRemoteUnmapped         Key = "doctor.path_rule.remote_unmapped"
	KeyDoctorPathRuleRemoteUnmappedHint     Key = "doctor.path_rule.remote_unmapped.hint"
	KeyDoctorPathRuleRemoteRootUnmapped     Key = "doctor.path_rule.remote_root_unmapped"
	KeyDoctorPathRuleRemoteRootUnmappedHint Key = "doctor.path_rule.remote_root_unmapped.hint"
)

// Libellés, sans paramètre, des codes de connexion, de capacité et de lecture distante d'un volume SFTP : ils servent
// de cause dans les contrôles doctor et de libellé de motif (ReasonLabel).
const (
	KeyReasonHostKeyUnknown                Key = "reason.host_key_unknown"
	KeyReasonHostKeyMismatch               Key = "reason.host_key_mismatch"
	KeyReasonAuthRefused                   Key = "reason.auth_refused"
	KeyReasonKnownHostsUnreadable          Key = "reason.known_hosts_unreadable"
	KeyReasonIdentityKeyTooWeak            Key = "reason.identity_key_too_weak"
	KeyReasonIdentityFileUnusable          Key = "reason.identity_file_unusable"
	KeyReasonIdentityFilePermissions       Key = "reason.identity_file_permissions"
	KeyReasonAgentUnavailable              Key = "reason.agent_unavailable"
	KeyReasonHostileResponse               Key = "reason.hostile_response"
	KeyReasonShortRead                     Key = "reason.short_read"
	KeyReasonRemoteWindowsUnsupported      Key = "reason.remote_windows_unsupported"
	KeyReasonNotProbed                     Key = "reason.not_probed"
	KeyReasonExtensionMissing              Key = "reason.extension_missing"
	KeyReasonProbeFailed                   Key = "reason.probe_failed"
	KeyReasonServerCopyUnsupportedByClient Key = "reason.server_copy_unsupported_by_client"
	KeyReasonRemoteExecUnavailable         Key = "reason.remote_exec_unavailable"
	KeyReasonExecOff                       Key = "reason.exec_off"
	KeyReasonExecDenied                    Key = "reason.exec_denied"
	KeyReasonLoginShellNotPOSIX            Key = "reason.login_shell_not_posix"
	KeyReasonExecProbeFailed               Key = "reason.exec_probe_failed"
	KeyReasonExecProgramMissing            Key = "reason.exec_program_missing"
	KeyReasonExecInconsistent              Key = "reason.exec_inconsistent"
	KeyReasonExecOutputTooLarge            Key = "reason.exec_output_too_large"
	KeyReasonExecPathOutsideRoots          Key = "reason.exec_path_outside_roots"
	KeyReasonExecSizeMismatch              Key = "reason.exec_size_mismatch"
	KeyReasonLinkDirWorldWritable          Key = "reason.link_dir_world_writable"
	KeyReasonLibraryRootUnreadable         Key = "reason.library_root_unreadable"
	KeyReasonNoMediaInFirst2000            Key = "reason.no_media_in_first_2000"
	KeyReasonLibraryMaybeWritable          Key = "reason.library_maybe_writable"
	KeyReasonVolumeIdentityUnknown         Key = "reason.volume_identity_unknown"
)

var doctorSFTPKeys = []Key{
	KeyDoctorTitleSFTP, KeyDoctorTitleSFTPLibrary, KeyDoctorTitleSFTPExec, KeyDoctorSFTPTarget, KeyDoctorSFTPOK,
	KeyDoctorSFTPHintKnownHosts, KeyDoctorSFTPHintHostKeyChanged, KeyDoctorSFTPHintAuth, KeyDoctorSFTPHintIdentity,
	KeyDoctorSFTPHintUnavailable, KeyDoctorSFTPHintWindows, KeyDoctorSFTPHintChmod, KeyDoctorSFTPHintAgent,
	KeyDoctorSFTPHintHostile, KeyDoctorSFTPLibraryNone, KeyDoctorSFTPLibraryRoot, KeyDoctorSFTPLibraryOK,
	KeyDoctorSFTPLibraryHintNoMedia, KeyDoctorSFTPLibraryHintUnread, KeyDoctorSFTPLibraryHintReadOnly,
	KeyDoctorSFTPExecOK, KeyDoctorSFTPExecOff, KeyDoctorSFTPExecLowPriority, KeyDoctorSFTPExecNoLowPriority,
	KeyDoctorSFTPExecNoLinkDir, KeyDoctorSFTPExecHintUnavailable, KeyDoctorSFTPExecHintRequired,
	KeyDoctorVolumeIdentityUnknown, KeyDoctorVolumeIdentityUnknownHint, KeyDoctorVolumeAliasLoopback,
	KeyDoctorVolumeAliasLoopbackHint, KeyDoctorVolumeAliasShared, KeyDoctorMountRootUndetected,
	KeyDoctorMountRootUndetectedHint, KeyDoctorPathRuleRemoteUnmapped,
	KeyDoctorPathRuleRemoteUnmappedHint, KeyDoctorPathRuleRemoteRootUnmapped, KeyDoctorPathRuleRemoteRootUnmappedHint,
	KeyReasonHostKeyUnknown, KeyReasonHostKeyMismatch, KeyReasonAuthRefused, KeyReasonKnownHostsUnreadable,
	KeyReasonIdentityKeyTooWeak, KeyReasonIdentityFileUnusable, KeyReasonIdentityFilePermissions,
	KeyReasonAgentUnavailable, KeyReasonHostileResponse, KeyReasonShortRead, KeyReasonRemoteWindowsUnsupported,
	KeyReasonNotProbed, KeyReasonExtensionMissing, KeyReasonProbeFailed, KeyReasonServerCopyUnsupportedByClient,
	KeyReasonRemoteExecUnavailable, KeyReasonExecOff, KeyReasonExecDenied, KeyReasonLoginShellNotPOSIX,
	KeyReasonExecProbeFailed, KeyReasonExecProgramMissing, KeyReasonExecInconsistent, KeyReasonExecOutputTooLarge,
	KeyReasonExecPathOutsideRoots, KeyReasonExecSizeMismatch, KeyReasonLinkDirWorldWritable,
	KeyReasonLibraryRootUnreadable, KeyReasonNoMediaInFirst2000, KeyReasonLibraryMaybeWritable,
	KeyReasonVolumeIdentityUnknown,
}
