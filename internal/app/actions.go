package app

import (
	"slices"

	"necromanciarr/internal/i18n"
	"necromanciarr/internal/pipeline"
)

// Action décrit une action du Service pour la CLI et le web : Name est la commande (« proposals accept »), Method la
// méthode de Service appelée, Mutating dit si elle modifie l'état (base, disque ou client torrent), Arguments liste les
// arguments obligatoires et Options les facultatifs.
type Action struct {
	Name   string `json:"name"`
	Method string `json:"method"`
	// DescriptionKey est la clé du texte ; Description est rempli à la sortie par Actions, dans la langue du Translator.
	DescriptionKey i18n.Key `json:"-"`
	Description    string   `json:"description"`
	Mutating       bool     `json:"mutating"`
	Arguments      []string `json:"arguments,omitempty"`
	Options        []string `json:"options,omitempty"`
}

// Noms des arguments et options partagés par la CLI et le web.
const (
	ArgumentStage            = "stage"
	ArgumentProposalID       = "proposal-id"
	ArgumentGroupID          = "group-id"
	OptionAllowPartialPack   = "allow-partial-pack"
	OptionDecision           = "decision"
	OptionKind               = "kind"
	OptionGroup              = "group"
	OptionCurrentOnly        = "current-only"
	OptionDryRun             = "dry-run"
	OptionAcceptPending      = "accept-pending"
	OptionDirectory          = "directory"
	OptionCheck              = "check"
	OptionState              = "state"
	OptionLimit              = "limit"
	OptionOffset             = "offset"
	actionNameProposalPrefix = "proposals "
)

var registry = []Action{
	{
		Name: "run", Method: "RunStage", DescriptionKey: i18n.KeyActionRun, Mutating: true,
		Arguments: []string{ArgumentStage},
	},
	{Name: "status", Method: "Status", DescriptionKey: i18n.KeyActionStatus},
	{
		Name: actionNameProposalPrefix + "list", Method: "ListProposals", DescriptionKey: i18n.KeyActionProposalsList,
		Options: []string{OptionDecision, OptionKind, OptionGroup, OptionCurrentOnly},
	},
	{
		Name: actionNameProposalPrefix + "accept", Method: "Accept", DescriptionKey: i18n.KeyActionProposalsAccept,
		Mutating: true, Arguments: []string{ArgumentProposalID}, Options: []string{OptionAllowPartialPack},
	},
	{
		Name: actionNameProposalPrefix + "reject", Method: "Reject", DescriptionKey: i18n.KeyActionProposalsReject, Mutating: true,
		Arguments: []string{ArgumentProposalID},
	},
	{
		Name: actionNameProposalPrefix + "choose", Method: "Choose", DescriptionKey: i18n.KeyActionProposalsChoose,
		Mutating: true, Arguments: []string{ArgumentProposalID},
	},
	{
		Name: actionNameProposalPrefix + "force", Method: "Force", DescriptionKey: i18n.KeyActionProposalsForce,
		Mutating: true, Arguments: []string{ArgumentProposalID},
	},
	{
		Name: actionNameProposalPrefix + "accept-group", Method: "AcceptGroup",
		DescriptionKey: i18n.KeyActionProposalsAcceptGrp, Mutating: true,
		Arguments: []string{ArgumentGroupID},
	},
	{
		Name: "dedupe plan", Method: "DedupePlan", Mutating: true,
		DescriptionKey: i18n.KeyActionDedupePlan,
	},
	{
		Name: "dedupe apply", Method: "DedupeAccept", DescriptionKey: i18n.KeyActionDedupeApply,
		Mutating: true, Arguments: []string{ArgumentProposalID}, Options: []string{OptionDryRun, OptionAcceptPending},
	},
	{
		Name: "repair scan", Method: "RepairScan", DescriptionKey: i18n.KeyActionRepairScan, Mutating: true,
		Options: []string{OptionDirectory},
	},
	{
		Name: "repair apply", Method: "RepairAccept", DescriptionKey: i18n.KeyActionRepairApply,
		Mutating: true, Arguments: []string{ArgumentProposalID}, Options: []string{OptionDryRun, OptionAcceptPending},
	},
	{
		Name: "links list", Method: "ListLinks", DescriptionKey: i18n.KeyActionLinksList,
		Options: []string{OptionState, OptionLimit, OptionOffset},
	},
	{
		Name: "doctor", Method: "Doctor", DescriptionKey: i18n.KeyActionDoctor,
		Mutating: true, Options: []string{OptionCheck},
	},
}

// Actions renvoie une copie du registre, dans l'ordre d'affichage, descriptions rendues par translator.
func Actions(translator i18n.Translator) []Action {
	actions := slices.Clone(registry)
	for index := range actions {
		actions[index].Description = translator.Text(actions[index].DescriptionKey, nil)
		actions[index].Arguments = slices.Clone(actions[index].Arguments)
		actions[index].Options = slices.Clone(actions[index].Options)
	}
	return actions
}

func (svc *service) Actions() []Action {
	return Actions(svc.deps.Translator)
}

// Stages liste les étapes que RunStage accepte, dans l'ordre d'exécution : le web les reconnaît par leur nom sans
// dépendre du pipeline.
func Stages() []pipeline.Stage {
	return []pipeline.Stage{pipeline.StageScan, pipeline.StageMatch, pipeline.StageVerify, pipeline.StageLink, pipeline.StageSeed}
}
