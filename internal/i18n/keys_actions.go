package i18n

// Clés des descriptions du registre des actions : « action. » suivi du nom de l'action, espaces remplacés par des points.
const (
	KeyActionRun                Key = "action.run"
	KeyActionStatus             Key = "action.status"
	KeyActionProposalsList      Key = "action.proposals.list"
	KeyActionProposalsAccept    Key = "action.proposals.accept"
	KeyActionProposalsReject    Key = "action.proposals.reject"
	KeyActionProposalsChoose    Key = "action.proposals.choose"
	KeyActionProposalsForce     Key = "action.proposals.force"
	KeyActionProposalsAcceptGrp Key = "action.proposals.accept-group"
	KeyActionDedupePlan         Key = "action.dedupe.plan"
	KeyActionDedupeApply        Key = "action.dedupe.apply"
	KeyActionRepairScan         Key = "action.repair.scan"
	KeyActionRepairApply        Key = "action.repair.apply"
	KeyActionLinksList          Key = "action.links.list"
	KeyActionDoctor             Key = "action.doctor"
)

var actionKeys = []Key{
	KeyActionRun, KeyActionStatus, KeyActionProposalsList, KeyActionProposalsAccept, KeyActionProposalsReject,
	KeyActionProposalsChoose, KeyActionProposalsForce, KeyActionProposalsAcceptGrp, KeyActionDedupePlan, KeyActionDedupeApply,
	KeyActionRepairScan, KeyActionRepairApply, KeyActionLinksList, KeyActionDoctor,
}
