package i18n

// Clés des textes de la ligne de commande.
const (
	KeyConfigCheckLanguage Key = "config.check.language"

	KeyCLIStageResult          Key = "cli.stage.result"
	KeyCLIStageReason          Key = "cli.stage.reason"
	KeyCLIStageSamples         Key = "cli.stage.unidentified_samples"
	KeyCLIStageFailedWarning   Key = "cli.stage.failed_warning"
	KeyCLICounterUnidentified  Key = "cli.counter.unidentified_files"
	KeyCLICounterCreated       Key = "cli.counter.candidates_created"
	KeyCLICounterUpdated       Key = "cli.counter.candidates_updated"
	KeyCLICounterUnchanged     Key = "cli.counter.candidates_unchanged"
	KeyCLICounterStale         Key = "cli.counter.candidates_stale"
	KeyCLICounterVerified      Key = "cli.counter.verified"
	KeyCLICounterPartialAnnex  Key = "cli.counter.partial_annex"
	KeyCLICounterPartialPack   Key = "cli.counter.partial_pack"
	KeyCLICounterConflicts     Key = "cli.counter.conflicts"
	KeyCLICounterResumed       Key = "cli.counter.resumed"
	KeyCLICounterProposals     Key = "cli.counter.proposals_created"
	KeyCLICounterLinked        Key = "cli.counter.linked"
	KeyCLICounterSeeded        Key = "cli.counter.seeded"
	KeyCLICounterSeededPaused  Key = "cli.counter.seeded_paused_incomplete"
	KeyCLILabelCandidates      Key = "cli.label.candidates"
	KeyCLILabelProposals       Key = "cli.label.proposals"
	KeyCLILabelAppliedSteps    Key = "cli.label.applied_steps"
	KeyCLILabelLinks           Key = "cli.label.links"
	KeyCLINone                 Key = "cli.none"
	KeyCLICountsLine           Key = "cli.counts.line"
	KeyCLIStatusCrossSeedMode  Key = "cli.status.cross_seed_mode"
	KeyCLIStatusHeld           Key = "cli.status.held"
	KeyCLIHeaderID             Key = "cli.header.id"
	KeyCLIHeaderKind           Key = "cli.header.kind"
	KeyCLIHeaderDecision       Key = "cli.header.decision"
	KeyCLIHeaderStep           Key = "cli.header.step"
	KeyCLIHeaderGroup          Key = "cli.header.group"
	KeyCLIHeaderName           Key = "cli.header.name"
	KeyCLIHeaderLink           Key = "cli.header.link"
	KeyCLIHeaderSaves          Key = "cli.header.saves"
	KeyCLIHeaderReads          Key = "cli.header.reads"
	KeyCLIHeaderMethod         Key = "cli.header.method"
	KeyCLIHeaderTorrentFile    Key = "cli.header.torrent_file"
	KeyCLIHeaderStatus         Key = "cli.header.status"
	KeyCLIHeaderState          Key = "cli.header.state"
	KeyCLIProposalsNone        Key = "cli.proposals.none"
	KeyCLIStalePrefix          Key = "cli.proposals.stale_prefix"
	KeyCLIDedupeNone           Key = "cli.dedupe.none"
	KeyCLIDedupeTotalRead      Key = "cli.dedupe.total_read"
	KeyCLIRepairNone           Key = "cli.repair.none"
	KeyCLIRepairNotActionable  Key = "cli.repair.not_actionable"
	KeyCLILinksNone            Key = "cli.links.none"
	KeyCLILinksShowing         Key = "cli.links.showing"
	KeyCLILinksNextPage        Key = "cli.links.next_page"
	KeyCLILinksTruncated       Key = "cli.links.truncated_warning"
	KeyCLIMaintenanceNone      Key = "cli.maintenance.none"
	KeyCLIMaintenanceFailed    Key = "cli.maintenance.failed"
	KeyCLIMaintenanceDryRun    Key = "cli.maintenance.dry_run"
	KeyCLIMaintenanceApplied   Key = "cli.maintenance.applied"
	KeyCLIProposalLine         Key = "cli.proposal_line"
	KeyCLIDoctorHint           Key = "cli.doctor.hint"
	KeyCLIDecided              Key = "cli.decided"
	KeyCLIGroupAccepted        Key = "cli.group.accepted"
	KeyCLIForceReplaced        Key = "cli.force.replaced"
	KeyCLIForceDone            Key = "cli.force.done"
	KeyCLIForceWarning         Key = "cli.force.warning"
	KeyCLISummaryStage         Key = "cli.summary.stage"
	KeyCLISummaryStatus        Key = "cli.summary.status"
	KeyCLISummaryProposalsList Key = "cli.summary.proposals_list"
	KeyCLISummaryAccept        Key = "cli.summary.proposals_accept"
	KeyCLISummaryAcceptGroup   Key = "cli.summary.proposals_accept_group"
	KeyCLISummaryReject        Key = "cli.summary.proposals_reject"
	KeyCLISummaryChoose        Key = "cli.summary.proposals_choose"
	KeyCLISummaryForce         Key = "cli.summary.proposals_force"
	KeyCLISummaryDedupePlan    Key = "cli.summary.dedupe_plan"
	KeyCLISummaryApply         Key = "cli.summary.apply"
	KeyCLISummaryRepairScan    Key = "cli.summary.repair_scan"
	KeyCLISummaryLinksList     Key = "cli.summary.links_list"
	KeyCLISummaryDoctor        Key = "cli.summary.doctor"
	KeyCLIUsageHeader          Key = "cli.usage.header"
	KeyCLIUsageServe           Key = "cli.usage.serve"
	KeyCLIUsageHealthcheck     Key = "cli.usage.healthcheck"
	KeyCLIUsageAPIKey          Key = "cli.usage.api_key"
	KeyCLIUsageConfigCheck     Key = "cli.usage.config_check"
	KeyCLIUsageExitCodes       Key = "cli.usage.exit_codes"
	KeyCLIUsageAllAccepted     Key = "cli.usage.all_accepted"
)

var cliKeys = []Key{
	KeyConfigCheckLanguage,
	KeyCLIStageResult, KeyCLIStageReason, KeyCLIStageSamples, KeyCLIStageFailedWarning,
	KeyCLICounterUnidentified, KeyCLICounterCreated, KeyCLICounterUpdated, KeyCLICounterUnchanged, KeyCLICounterStale,
	KeyCLICounterVerified, KeyCLICounterPartialAnnex, KeyCLICounterPartialPack, KeyCLICounterConflicts, KeyCLICounterResumed,
	KeyCLICounterProposals, KeyCLICounterLinked, KeyCLICounterSeeded, KeyCLICounterSeededPaused,
	KeyCLILabelCandidates, KeyCLILabelProposals, KeyCLILabelAppliedSteps, KeyCLILabelLinks, KeyCLINone, KeyCLICountsLine,
	KeyCLIStatusCrossSeedMode, KeyCLIStatusHeld,
	KeyCLIHeaderID, KeyCLIHeaderKind, KeyCLIHeaderDecision, KeyCLIHeaderStep, KeyCLIHeaderGroup, KeyCLIHeaderName,
	KeyCLIHeaderLink, KeyCLIHeaderSaves, KeyCLIHeaderReads, KeyCLIHeaderMethod, KeyCLIHeaderTorrentFile,
	KeyCLIHeaderStatus, KeyCLIHeaderState,
	KeyCLIProposalsNone, KeyCLIStalePrefix, KeyCLIDedupeNone, KeyCLIDedupeTotalRead, KeyCLIRepairNone, KeyCLIRepairNotActionable,
	KeyCLILinksNone, KeyCLILinksShowing, KeyCLILinksNextPage, KeyCLILinksTruncated,
	KeyCLIMaintenanceNone, KeyCLIMaintenanceFailed, KeyCLIMaintenanceDryRun, KeyCLIMaintenanceApplied,
	KeyCLIProposalLine, KeyCLIDoctorHint, KeyCLIDecided, KeyCLIGroupAccepted,
	KeyCLIForceReplaced, KeyCLIForceDone, KeyCLIForceWarning,
	KeyCLISummaryStage, KeyCLISummaryStatus, KeyCLISummaryProposalsList, KeyCLISummaryAccept, KeyCLISummaryAcceptGroup,
	KeyCLISummaryReject, KeyCLISummaryChoose, KeyCLISummaryForce, KeyCLISummaryDedupePlan, KeyCLISummaryApply,
	KeyCLISummaryRepairScan, KeyCLISummaryLinksList, KeyCLISummaryDoctor,
	KeyCLIUsageHeader, KeyCLIUsageServe, KeyCLIUsageHealthcheck, KeyCLIUsageAPIKey,
	KeyCLIUsageConfigCheck, KeyCLIUsageExitCodes, KeyCLIUsageAllAccepted,
}
