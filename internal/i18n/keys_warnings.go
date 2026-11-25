package i18n

// Clés des avertissements stockés sur les propositions (clé et paramètres en base).
const (
	KeyWarningIncompleteAfterRecheck       Key = "warning.incomplete_after_recheck"
	KeyWarningCrossSeedNotifyFailed        Key = "warning.crossseed_notify_failed"
	KeyWarningSourcesChanged               Key = "warning.sources_changed"
	KeyWarningAnnexHashFailed              Key = "warning.annex_hash_failed"
	KeyWarningPackMediaAbsent              Key = "warning.pack_media_absent"
	KeyWarningAnnexPiecesNotProven         Key = "warning.annex_pieces_not_proven"
	KeyWarningApplicationFailed            Key = "warning.application_failed"
	KeyWarningApplicationFailedCrossDevice Key = "warning.application_failed.cross_device"
	KeyWarningApplicationFailedNoLinkDir   Key = "warning.application_failed.no_link_dir"
	KeyWarningApplicationFailedKindRefused Key = "warning.application_failed.kind_refused"
	KeyWarningApplicationFailedDetail      Key = "warning.application_failed.detail"
	KeyWarningPausedIncompleteAfterRecheck Key = "warning.paused_incomplete_after_recheck"
	KeyWarningRecheckFailed                Key = "warning.recheck_failed"
	KeyWarningNoClientRecheck              Key = "warning.no_client_recheck"
	KeyWarningLinkNotRecorded              Key = "warning.link_not_recorded"

	KeyWarningDedupeUnreliableInode        Key = "warning.dedupe.unreliable_inode"
	KeyWarningDedupeFullNetworkRead        Key = "warning.dedupe.full_network_read"
	KeyWarningDedupeSampledVerify          Key = "warning.dedupe.sampled_verify"
	KeyWarningDedupeRenameNotAtomic        Key = "warning.dedupe.rename_not_atomic"
	KeyWarningDedupeFullRemoteRead         Key = "warning.dedupe.full_remote_read"
	KeyWarningCrossSeedDelegateNoDaemon    Key = "warning.crossseed.delegate_no_daemon"
	KeyWarningCrossSeedDelegateUnreachable Key = "warning.crossseed.delegate_unreachable"
	KeyWarningCrossSeedFallbackNative      Key = "warning.crossseed.fallback_native"
	KeyWarningMultipleClients              Key = "warning.multiple_clients"
	// KeyWarningNoAnnounce est calculé à la lecture (jamais stocké) : paramètre tracker.
	KeyWarningNoAnnounce Key = "warning.no_announce"
	// KeyWarningNoAnnounceUnknownTracker nomme un tracker vide dans warning.no_announce.
	KeyWarningNoAnnounceUnknownTracker Key = "warning.no_announce.unknown_tracker"
)

// Conseils sur un montage réseau (doctor, contrôle rclone) : produits par hashsource sous forme de messages.
const (
	KeyAdviceMountStartRC        Key = "advice.mount.start_rc"
	KeyAdviceMountNoHash         Key = "advice.mount.no_hash"
	KeyAdviceMountNoModTime      Key = "advice.mount.no_mod_time"
	KeyAdviceMountModTimeUnknown Key = "advice.mount.mod_time_unknown"
	KeyAdviceMountVFSLinks       Key = "advice.mount.vfs_links"
	KeyAdviceMountRcloneError    Key = "advice.mount.rclone_error"
	KeyAdviceMountNoSample       Key = "advice.mount.no_sample"
	KeyAdviceMountInvalid        Key = "advice.mount.invalid"
)

var warningKeys = []Key{
	KeyWarningIncompleteAfterRecheck, KeyWarningCrossSeedNotifyFailed, KeyWarningSourcesChanged,
	KeyWarningAnnexHashFailed, KeyWarningPackMediaAbsent, KeyWarningAnnexPiecesNotProven,
	KeyWarningApplicationFailed, KeyWarningApplicationFailedCrossDevice, KeyWarningApplicationFailedNoLinkDir,
	KeyWarningApplicationFailedKindRefused, KeyWarningApplicationFailedDetail, KeyWarningPausedIncompleteAfterRecheck,
	KeyWarningRecheckFailed, KeyWarningNoClientRecheck, KeyWarningLinkNotRecorded,
	KeyWarningDedupeUnreliableInode, KeyWarningDedupeFullNetworkRead, KeyWarningDedupeSampledVerify,
	KeyWarningDedupeRenameNotAtomic, KeyWarningDedupeFullRemoteRead, KeyWarningCrossSeedDelegateNoDaemon, KeyWarningCrossSeedDelegateUnreachable,
	KeyWarningCrossSeedFallbackNative, KeyWarningMultipleClients, KeyWarningNoAnnounce, KeyWarningNoAnnounceUnknownTracker,
	KeyAdviceMountStartRC, KeyAdviceMountNoHash, KeyAdviceMountNoModTime, KeyAdviceMountModTimeUnknown,
	KeyAdviceMountVFSLinks, KeyAdviceMountRcloneError, KeyAdviceMountNoSample, KeyAdviceMountInvalid,
}
