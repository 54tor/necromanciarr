package i18n

// Clés des erreurs : une par erreur typée reconnue par app.Classify, puis celles du web lui-même.
const (
	KeyErrorUsageUnknownLanguage Key = "error.usage.unknown_language"
	KeyErrorInternal             Key = "error.internal"
	// KeyErrorUsageInvalidSecretKey et KeyErrorConfigSecretKeyRequired ne citent que le nom de la variable.
	KeyErrorUsageInvalidSecretKey   Key = "error.usage.invalid_secret_key"
	KeyErrorConfigSecretKeyRequired Key = "error.config.secret_key_required"

	KeyErrorUsage                    Key = "error.usage"
	KeyErrorUnknownCheck             Key = "error.unknown_check"
	KeyErrorStaleProposal            Key = "error.stale_proposal"
	KeyErrorConflictNeedsChoose      Key = "error.conflict_needs_choose"
	KeyErrorPartialPackNeedsForce    Key = "error.partial_pack_needs_force"
	KeyErrorNotATorrentProposal      Key = "error.not_a_torrent_proposal"
	KeyErrorCandidateAlreadyAccepted Key = "error.candidate_already_accepted"
	KeyErrorOneTorrentPerFile        Key = "error.one_torrent_per_file"
	KeyErrorNotFull                  Key = "error.not_full"
	KeyErrorNotForceable             Key = "error.not_forceable"
	KeyErrorNoPrimary                Key = "error.no_primary"
	KeyErrorForceNotAllowed          Key = "error.force_not_allowed"
	KeyErrorForceIsolationImpossible Key = "error.force_isolation_impossible"
	KeyErrorCrossSeedNotNative       Key = "error.cross_seed_not_native"
	KeyErrorNotAccepted              Key = "error.not_accepted"
	KeyErrorNotActionable            Key = "error.not_actionable"
	KeyErrorWrongProposalKind        Key = "error.wrong_proposal_kind"
	KeyErrorDirectoryInLibrary       Key = "error.directory_in_library"
	KeyErrorRecheckUnavailable       Key = "error.recheck_unavailable"
	KeyErrorInvalidTransition        Key = "error.invalid_transition"
	KeyErrorGroupInconsistent        Key = "error.group_inconsistent"
	KeyErrorClientReadOnly           Key = "error.client_read_only"
	KeyErrorPlacementChanged         Key = "error.placement_changed"
	KeyErrorTorrentRootOverlaps      Key = "error.torrent_root_overlaps_library"
	KeyErrorOutsideRoots             Key = "error.outside_roots"
	KeyErrorProposalNotFound         Key = "error.proposal_not_found"
	KeyErrorGroupNotFound            Key = "error.group_not_found"
	KeyErrorNotConfigured            Key = "error.not_configured"
	KeyErrorStageRunning             Key = "error.stage_running"
	KeyErrorInspectionBusy           Key = "error.inspection_busy"
	KeyErrorSourceNotFound           Key = "error.source_not_found"
	KeyErrorSourceNotTestable        Key = "error.source_not_testable"
	KeyErrorSourceTestRunning        Key = "error.source_test_running"

	KeyErrorWebUnknownHost         Key = "error.web.unknown_host"
	KeyErrorWebCrossSite           Key = "error.web.cross_site"
	KeyErrorWebBearerToken         Key = "error.web.bearer_token"
	KeyErrorWebContentType         Key = "error.web.content_type"
	KeyErrorWebUnknownRoute        Key = "error.web.unknown_route"
	KeyErrorWebBodyTooLarge        Key = "error.web.body_too_large"
	KeyErrorWebInvalidJSON         Key = "error.web.invalid_json"
	KeyErrorWebPositiveInteger     Key = "error.web.positive_integer"
	KeyErrorWebNonNegativeInteger  Key = "error.web.non_negative_integer"
	KeyErrorWebUnknownQuery        Key = "error.web.unknown_query"
	KeyErrorWebBoolean             Key = "error.web.boolean"
	KeyErrorWebCandidateNotInGroup Key = "error.web.candidate_not_in_proposal"
	KeyErrorWebConfirmRequired     Key = "error.web.confirm_required"
	KeyErrorWebUnknownStage        Key = "error.web.unknown_stage"
	KeyErrorWebJobNotFound         Key = "error.web.job_not_found"
	KeyErrorWebJobRunning          Key = "error.web.job_running"
	KeyErrorWebShuttingDown        Key = "error.web.shutting_down"
	KeyErrorWebMethodNotAllowed    Key = "error.web.method_not_allowed"
)

var errorKeys = []Key{
	KeyErrorUsageUnknownLanguage, KeyErrorInternal, KeyErrorUsageInvalidSecretKey, KeyErrorConfigSecretKeyRequired,
	KeyErrorUsage, KeyErrorUnknownCheck, KeyErrorStaleProposal, KeyErrorConflictNeedsChoose, KeyErrorPartialPackNeedsForce,
	KeyErrorNotATorrentProposal, KeyErrorCandidateAlreadyAccepted, KeyErrorOneTorrentPerFile, KeyErrorNotFull, KeyErrorNotForceable,
	KeyErrorNoPrimary, KeyErrorForceNotAllowed, KeyErrorForceIsolationImpossible, KeyErrorCrossSeedNotNative, KeyErrorNotAccepted,
	KeyErrorNotActionable, KeyErrorWrongProposalKind, KeyErrorDirectoryInLibrary, KeyErrorRecheckUnavailable,
	KeyErrorInvalidTransition, KeyErrorGroupInconsistent, KeyErrorClientReadOnly, KeyErrorPlacementChanged,
	KeyErrorTorrentRootOverlaps, KeyErrorOutsideRoots, KeyErrorProposalNotFound, KeyErrorGroupNotFound, KeyErrorNotConfigured,
	KeyErrorStageRunning, KeyErrorInspectionBusy, KeyErrorSourceNotFound, KeyErrorSourceNotTestable,
	KeyErrorSourceTestRunning,
	KeyErrorWebUnknownHost, KeyErrorWebCrossSite, KeyErrorWebBearerToken, KeyErrorWebContentType, KeyErrorWebUnknownRoute,
	KeyErrorWebBodyTooLarge, KeyErrorWebInvalidJSON, KeyErrorWebPositiveInteger, KeyErrorWebNonNegativeInteger,
	KeyErrorWebUnknownQuery, KeyErrorWebBoolean, KeyErrorWebCandidateNotInGroup, KeyErrorWebConfirmRequired,
	KeyErrorWebUnknownStage, KeyErrorWebJobNotFound, KeyErrorWebJobRunning, KeyErrorWebShuttingDown, KeyErrorWebMethodNotAllowed,
}
