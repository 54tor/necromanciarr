package i18n

// Clés des libellés de motifs : « reason. » suivi du code, qui reste le seul contenu JSON stable.
const (
	KeyReasonV2Only                      Key = "reason.v2_only"
	KeyReasonAddFailed                   Key = "reason.add_failed"
	KeyReasonAlreadyInClient             Key = "reason.already_in_client"
	KeyReasonAlreadyLinked               Key = "reason.already_linked"
	KeyReasonAnnexChanged                Key = "reason.annex_changed"
	KeyReasonAnnexWriteFailed            Key = "reason.annex_write_failed"
	KeyReasonArrPathOutsideLibrary       Key = "reason.arr_path_outside_library"
	KeyReasonArrPathsMatchedNothing      Key = "reason.arr_paths_matched_nothing"
	KeyReasonAssignmentMismatch          Key = "reason.assignment_mismatch"
	KeyReasonCandidateAlreadyLinked      Key = "reason.candidate_already_linked"
	KeyReasonCandidateNotVerified        Key = "reason.candidate_not_verified"
	KeyReasonChangedFileStale            Key = "reason.changed_file_stale"
	KeyReasonChangedFileInvalidated      Key = "reason.changed_file_invalidated"
	KeyReasonClientPathInvalid           Key = "reason.client_path_invalid"
	KeyReasonClientReadOnly              Key = "reason.client_read_only"
	KeyReasonConflictingIds              Key = "reason.conflicting_ids"
	KeyReasonCrossDevice                 Key = "reason.cross_device"
	KeyReasonCrossseedNotifyFailed       Key = "reason.crossseed_notify_failed"
	KeyReasonDestinationConflict         Key = "reason.destination_conflict"
	KeyReasonEmptyRootNotPruned          Key = "reason.empty_root_not_pruned"
	KeyReasonIncompleteAfterRecheck      Key = "reason.incomplete_after_recheck"
	KeyReasonInfohashMismatch            Key = "reason.infohash_mismatch"
	KeyReasonInvalidTorrent              Key = "reason.invalid_torrent"
	KeyReasonLinkChanged                 Key = "reason.link_changed"
	KeyReasonLinkDirOverlapsLibrary      Key = "reason.link_dir_overlaps_library"
	KeyReasonLinkFailed                  Key = "reason.link_failed"
	KeyReasonLinkKindRefused             Key = "reason.link_kind_refused"
	KeyReasonLinkRootUnusable            Key = "reason.link_root_unusable"
	KeyReasonMediaHashFailed             Key = "reason.media_hash_failed"
	KeyReasonMediaNotVerified            Key = "reason.media_not_verified"
	KeyReasonMissingFileRemoved          Key = "reason.missing_file_removed"
	KeyReasonMissingPiecesAboveCap       Key = "reason.missing_pieces_above_cap"
	KeyReasonNfoFetchFailed              Key = "reason.nfo_fetch_failed"
	KeyReasonNoExactSize                 Key = "reason.no_exact_size"
	KeyReasonNoExternalid                Key = "reason.no_external_id"
	KeyReasonNoFileMatched               Key = "reason.no_file_matched"
	KeyReasonNoLinkDirOnSameDevice       Key = "reason.no_link_dir_on_same_device"
	KeyReasonNotATorrentProposal         Key = "reason.not_a_torrent_proposal"
	KeyReasonNotLinked                   Key = "reason.not_linked"
	KeyReasonNotPausedAfterAdd           Key = "reason.not_paused_after_add"
	KeyReasonNoVerifiedAssignment        Key = "reason.no_verified_assignment"
	KeyReasonPausedIncomplete            Key = "reason.paused_incomplete"
	KeyReasonPauseFailed                 Key = "reason.pause_failed"
	KeyReasonReadBudgetExceeded          Key = "reason.read_budget_exceeded"
	KeyReasonReadError                   Key = "reason.read_error"
	KeyReasonRecheckFailed               Key = "reason.recheck_failed"
	KeyReasonResumeFailed                Key = "reason.resume_failed"
	KeyReasonSearchFailed                Key = "reason.search_failed"
	KeyReasonSourceChanged               Key = "reason.source_changed"
	KeyReasonStaleCandidate              Key = "reason.stale_candidate"
	KeyReasonStaleGeneration             Key = "reason.stale_generation"
	KeyReasonTorrentFetchFailed          Key = "reason.torrent_fetch_failed"
	KeyReasonTorrentNotFound             Key = "reason.torrent_not_found"
	KeyReasonTorrentTooLarge             Key = "reason.torrent_too_large"
	KeyReasonUpdateDeferredIncompleteRun Key = "reason.update_deferred_incomplete_run"
	KeyReasonAnnexHashFailed             Key = "reason.annex_hash_failed"
	KeyReasonNeedsClientRecheck          Key = "reason.needs_client_recheck"
	KeyReasonIsolatedCopy                Key = "reason.isolated_copy"

	KeyReasonLinkMissing          Key = "reason.link_missing"
	KeyReasonLinkTargetMissing    Key = "reason.link_target_missing"
	KeyReasonLinkInodeDiverged    Key = "reason.link_inode_diverged"
	KeyReasonLinkTargetRenamed    Key = "reason.link_target_renamed"
	KeyReasonLinkNotCompared      Key = "reason.link_not_compared"
	KeyReasonLinkReplaced         Key = "reason.link_replaced"
	KeyReasonLinkResized          Key = "reason.link_resized"
	KeyReasonLinkTargetUnreadable Key = "reason.link_target_unreadable"
	KeyReasonLinkInspectionFailed Key = "reason.link_inspection_failed"
	KeyReasonHashUnavailable      Key = "reason.hash_unavailable"
	KeyReasonNoLinkKind           Key = "reason.no_link_kind"
	KeyReasonLinkKindUnsupported  Key = "reason.link_kind_unsupported"
	KeyReasonLinkProbeFailed      Key = "reason.link_probe_failed"

	KeyReasonNoAnnounce                 Key = "reason.no_announce"
	KeyReasonSealedTorrentUnreadable    Key = "reason.sealed_torrent_unreadable"
	KeyReasonSecretKeyMissing           Key = "reason.secret_key_missing"
	KeyReasonInfohashChangedAfterInject Key = "reason.infohash_changed_after_inject"
	KeyReasonIndexerBudgetExhausted     Key = "reason.indexer_budget_exhausted"
)

var reasonKeys = []Key{
	KeyReasonV2Only,
	KeyReasonAddFailed, KeyReasonAlreadyInClient, KeyReasonAlreadyLinked, KeyReasonAnnexChanged,
	KeyReasonAnnexWriteFailed, KeyReasonArrPathOutsideLibrary, KeyReasonArrPathsMatchedNothing, KeyReasonAssignmentMismatch,
	KeyReasonCandidateAlreadyLinked, KeyReasonCandidateNotVerified, KeyReasonChangedFileStale, KeyReasonChangedFileInvalidated,
	KeyReasonClientPathInvalid, KeyReasonClientReadOnly, KeyReasonConflictingIds, KeyReasonCrossDevice,
	KeyReasonCrossseedNotifyFailed, KeyReasonDestinationConflict, KeyReasonEmptyRootNotPruned, KeyReasonIncompleteAfterRecheck,
	KeyReasonInfohashMismatch, KeyReasonInvalidTorrent, KeyReasonLinkChanged, KeyReasonLinkDirOverlapsLibrary,
	KeyReasonLinkFailed, KeyReasonLinkKindRefused, KeyReasonLinkRootUnusable, KeyReasonMediaHashFailed,
	KeyReasonMediaNotVerified, KeyReasonMissingFileRemoved, KeyReasonMissingPiecesAboveCap, KeyReasonNfoFetchFailed,
	KeyReasonNoExactSize, KeyReasonNoExternalid, KeyReasonNoFileMatched, KeyReasonNoLinkDirOnSameDevice,
	KeyReasonNotATorrentProposal, KeyReasonNotLinked, KeyReasonNotPausedAfterAdd, KeyReasonNoVerifiedAssignment,
	KeyReasonPausedIncomplete, KeyReasonPauseFailed, KeyReasonReadBudgetExceeded, KeyReasonReadError,
	KeyReasonRecheckFailed, KeyReasonResumeFailed, KeyReasonSearchFailed, KeyReasonSourceChanged,
	KeyReasonStaleCandidate, KeyReasonStaleGeneration, KeyReasonTorrentFetchFailed, KeyReasonTorrentNotFound,
	KeyReasonTorrentTooLarge, KeyReasonUpdateDeferredIncompleteRun, KeyReasonAnnexHashFailed, KeyReasonNeedsClientRecheck,
	KeyReasonIsolatedCopy,
	KeyReasonLinkMissing, KeyReasonLinkTargetMissing, KeyReasonLinkInodeDiverged, KeyReasonLinkTargetRenamed,
	KeyReasonLinkNotCompared, KeyReasonLinkReplaced, KeyReasonLinkResized, KeyReasonLinkTargetUnreadable,
	KeyReasonLinkInspectionFailed, KeyReasonHashUnavailable, KeyReasonNoLinkKind, KeyReasonLinkKindUnsupported,
	KeyReasonLinkProbeFailed,
	KeyReasonNoAnnounce, KeyReasonSealedTorrentUnreadable, KeyReasonSecretKeyMissing, KeyReasonInfohashChangedAfterInject,
	KeyReasonIndexerBudgetExhausted,
}
