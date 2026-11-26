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
	ArgumentSourceName       = "name"
	ArgumentVolumeName       = "name"
	OptionAllowPartialPack   = "allow-partial-pack"
	OptionDecision           = "decision"
	OptionKind               = "kind"
	OptionGroup              = "group"
	OptionSource             = "source"
	OptionCurrentOnly        = "current-only"
	OptionDryRun             = "dry-run"
	OptionAcceptPending      = "accept-pending"
	OptionDirectory          = "directory"
	OptionCheck              = "check"
	OptionState              = "state"
	OptionLimit              = "limit"
	OptionOffset             = "offset"
	ArgumentConfigKey        = "key"
	ArgumentConfigValue      = "value"
	ArgumentConfigList       = "list"
	ArgumentConfigItemID     = "item-id"
	ArgumentConfigElement    = "element"
	actionNameProposalPrefix = "proposals "
	actionNameConfigPrefix   = "config "
)

var registry = []Action{
	{
		Name: "run", Method: "RunStage", DescriptionKey: i18n.KeyActionRun, Mutating: true,
		Arguments: []string{ArgumentStage},
	},
	{Name: "status", Method: "Status", DescriptionKey: i18n.KeyActionStatus},
	{
		Name: actionNameProposalPrefix + "list", Method: "ListProposals", DescriptionKey: i18n.KeyActionProposalsList,
		Options: []string{OptionDecision, OptionKind, OptionGroup, OptionCurrentOnly, OptionSource},
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
	{Name: "sources list", Method: "ListSources", DescriptionKey: i18n.KeyActionSourcesList},
	// sources test dépense le quota du jour de l'indexeur et le joint : mutante, comme doctor.
	{
		Name: "sources test", Method: "TestSource", DescriptionKey: i18n.KeyActionSourcesTest,
		Mutating: true, Arguments: []string{ArgumentSourceName},
	},
	{Name: "volumes list", Method: "ListVolumes", DescriptionKey: i18n.KeyActionVolumesList},
	// volumes probe écrit des fichiers de sonde dans un dossier de liens distant : mutante, comme doctor.
	{
		Name: "volumes probe", Method: "ProbeVolume", DescriptionKey: i18n.KeyActionVolumesProbe,
		Mutating: true, Arguments: []string{ArgumentVolumeName},
	},
	{
		Name: "volumes commands", Method: "VolumeCommands", DescriptionKey: i18n.KeyActionVolumesCommands,
		Arguments: []string{ArgumentVolumeName},
	},
	{Name: actionNameConfigPrefix + "get", Method: "ConfigView", DescriptionKey: i18n.KeyActionConfigGet},
	{
		Name: actionNameConfigPrefix + "set", Method: "SetSetting", DescriptionKey: i18n.KeyActionConfigSet, Mutating: true,
		Arguments: []string{ArgumentConfigKey, ArgumentConfigValue},
	},
	{
		Name: actionNameConfigPrefix + "unset", Method: "UnsetSetting", DescriptionKey: i18n.KeyActionConfigUnset,
		Mutating: true, Arguments: []string{ArgumentConfigKey},
	},
	{
		Name: actionNameConfigPrefix + "item-set", Method: "PutItem", DescriptionKey: i18n.KeyActionConfigItemSet,
		Mutating: true, Arguments: []string{ArgumentConfigList, ArgumentConfigItemID, ArgumentConfigElement},
	},
	{
		Name: actionNameConfigPrefix + "item-delete", Method: "DeleteItem", DescriptionKey: i18n.KeyActionConfigItemDelete,
		Mutating: true, Arguments: []string{ArgumentConfigList, ArgumentConfigItemID},
	},
	{
		Name: actionNameConfigPrefix + "item-unset", Method: "UnsetItem", DescriptionKey: i18n.KeyActionConfigItemUnset,
		Mutating: true, Arguments: []string{ArgumentConfigList, ArgumentConfigItemID},
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
