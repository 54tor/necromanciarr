package app

import "slices"

// Action décrit une action du Service pour la CLI et le web : Name est la commande (« proposals accept »), Method la
// méthode de Service appelée, Mutating dit si elle modifie l'état (base, disque ou client torrent), Arguments liste les
// arguments obligatoires et Options les facultatifs.
type Action struct {
	Name        string   `json:"name"`
	Method      string   `json:"method"`
	Description string   `json:"description"`
	Mutating    bool     `json:"mutating"`
	Arguments   []string `json:"arguments,omitempty"`
	Options     []string `json:"options,omitempty"`
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
	actionNameProposalPrefix = "proposals "
)

var registry = []Action{
	{
		Name: "run", Method: "RunStage", Description: "run one pipeline stage (scan, match, verify, link, seed)", Mutating: true,
		Arguments: []string{ArgumentStage},
	},
	{Name: "status", Method: "Status", Description: "summarize candidates, proposals and the cross-seed mode"},
	{
		Name: actionNameProposalPrefix + "list", Method: "ListProposals", Description: "list proposals with their details",
		Options: []string{OptionDecision, OptionKind, OptionGroup, OptionCurrentOnly},
	},
	{
		Name: actionNameProposalPrefix + "accept", Method: "Accept", Description: "accept a torrent proposal of the current generation",
		Mutating: true, Arguments: []string{ArgumentProposalID}, Options: []string{OptionAllowPartialPack},
	},
	{
		Name: actionNameProposalPrefix + "reject", Method: "Reject", Description: "reject a proposal", Mutating: true,
		Arguments: []string{ArgumentProposalID},
	},
	{
		Name: actionNameProposalPrefix + "choose", Method: "Choose", Description: "make a full proposal the primary of its group",
		Mutating: true, Arguments: []string{ArgumentProposalID},
	},
	{
		Name: actionNameProposalPrefix + "force", Method: "Force", Description: "start a linked partial_annex left paused by seed",
		Mutating: true, Arguments: []string{ArgumentProposalID},
	},
	{
		Name: actionNameProposalPrefix + "accept-group", Method: "AcceptGroup",
		Description: "accept the primary of a group, and the other verified torrents in native cross-seed mode", Mutating: true,
		Arguments: []string{ArgumentGroupID},
	},
}

// Actions renvoie une copie du registre, dans l'ordre d'affichage.
func Actions() []Action {
	actions := slices.Clone(registry)
	for index := range actions {
		actions[index].Arguments = slices.Clone(actions[index].Arguments)
		actions[index].Options = slices.Clone(actions[index].Options)
	}
	return actions
}

func (svc *service) Actions() []Action {
	return Actions()
}
