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
)

var warningKeys = []Key{
	KeyWarningIncompleteAfterRecheck, KeyWarningCrossSeedNotifyFailed, KeyWarningSourcesChanged,
	KeyWarningAnnexHashFailed, KeyWarningPackMediaAbsent, KeyWarningAnnexPiecesNotProven,
	KeyWarningApplicationFailed, KeyWarningApplicationFailedCrossDevice, KeyWarningApplicationFailedNoLinkDir,
	KeyWarningApplicationFailedKindRefused, KeyWarningApplicationFailedDetail, KeyWarningPausedIncompleteAfterRecheck,
	KeyWarningRecheckFailed, KeyWarningNoClientRecheck, KeyWarningLinkNotRecorded,
}
