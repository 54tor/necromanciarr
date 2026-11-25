package app

import (
	"errors"

	"necromanciarr/internal/config"
	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/repair"
	"necromanciarr/internal/store"
)

// Codes d'erreur stables partagés par la sortie JSON de la CLI et l'API web ({"error": {"code", "message"}}).
const (
	ErrorCodeUsage          = "usage"
	ErrorCodeStale          = "stale_proposal"
	ErrorCodeRefused        = "refused"
	ErrorCodeNotFound       = "not_found"
	ErrorCodeNotConfigured  = "not_configured"
	ErrorCodeStageRunning   = "stage_running"
	ErrorCodeInspectionBusy = "inspection_busy"
	ErrorCodeFailure        = "failure"
)

// refusals sont les refus typés des règles métier : la demande est bien formée, l'action ne l'est pas.
var refusals = []error{
	ErrConflictNeedsChoose, ErrPartialPackNeedsForce, ErrNotATorrentProposal, ErrCandidateAlreadyAccepted,
	ErrOneTorrentPerFile, ErrNotFull, ErrNotForceable, ErrNoPrimary, ErrForceNotAllowed, ErrForceServerCopyUnavailable,
	ErrForceFreeSpaceUnknown, ErrForceIsolationImpossible, ErrCrossSeedNotNative,
	ErrNotAccepted, ErrNotActionable, ErrWrongProposalKind, ErrDirectoryInLibrary, ErrRecheckUnavailable,
	store.ErrInvalidTransition, pipeline.ErrGroupInconsistent, pipeline.ErrClientReadOnly, pipeline.ErrPlacementChanged,
	dedupe.ErrTorrentRootOverlapsLibrary, repair.ErrOutsideRoots, ErrSourceNotTestable, ErrSourceTestRunning,
	ErrVolumeProbeRunning,
}

// classes associe chaque code aux erreurs typées qu'il couvre, dans l'ordre de priorité du classement.
var classes = []struct {
	code   string
	errors []error
}{
	{ErrorCodeUsage, []error{config.ErrUsage, ErrUnknownCheck}},
	{ErrorCodeStale, []error{ErrStaleProposal}},
	{ErrorCodeRefused, refusals},
	{ErrorCodeNotFound, []error{store.ErrProposalNotFound, ErrGroupNotFound, ErrSourceNotFound, ErrVolumeNotFound}},
	{ErrorCodeNotConfigured, []error{ErrNotConfigured}},
	{ErrorCodeStageRunning, []error{pipeline.ErrStageRunning}},
	{ErrorCodeInspectionBusy, []error{ErrInspectionBusy}},
}

// Classify donne le code stable d'une erreur et l'erreur typée reconnue dans sa chaîne (nil pour ErrorCodeFailure) :
// le web n'affiche que le message de cette erreur typée, jamais le texte brut des couches basses.
func Classify(err error) (string, error) {
	for _, class := range classes {
		for _, typed := range class.errors {
			if errors.Is(err, typed) {
				return class.code, typed
			}
		}
	}
	return ErrorCodeFailure, nil
}

// messageKeys donne la clé de catalogue de chaque erreur typée de classes : le texte rendu ne dépend jamais du message
// anglais de l'erreur, qui peut porter un chemin ou le texte d'une couche basse.
var messageKeys = map[error]i18n.Key{
	config.ErrUsage: i18n.KeyErrorUsage, ErrUnknownCheck: i18n.KeyErrorUnknownCheck, ErrStaleProposal: i18n.KeyErrorStaleProposal,
	ErrConflictNeedsChoose: i18n.KeyErrorConflictNeedsChoose, ErrPartialPackNeedsForce: i18n.KeyErrorPartialPackNeedsForce,
	ErrNotATorrentProposal: i18n.KeyErrorNotATorrentProposal, ErrCandidateAlreadyAccepted: i18n.KeyErrorCandidateAlreadyAccepted,
	ErrOneTorrentPerFile: i18n.KeyErrorOneTorrentPerFile, ErrNotFull: i18n.KeyErrorNotFull, ErrNotForceable: i18n.KeyErrorNotForceable,
	ErrNoPrimary: i18n.KeyErrorNoPrimary, ErrForceNotAllowed: i18n.KeyErrorForceNotAllowed,
	ErrForceIsolationImpossible: i18n.KeyErrorForceIsolationImpossible, ErrCrossSeedNotNative: i18n.KeyErrorCrossSeedNotNative,
	ErrForceServerCopyUnavailable: i18n.KeyReasonServerCopyUnavailable, ErrForceFreeSpaceUnknown: i18n.KeyReasonFreeSpaceUnknown,
	ErrNotAccepted: i18n.KeyErrorNotAccepted, ErrNotActionable: i18n.KeyErrorNotActionable,
	ErrWrongProposalKind: i18n.KeyErrorWrongProposalKind, ErrDirectoryInLibrary: i18n.KeyErrorDirectoryInLibrary,
	ErrRecheckUnavailable: i18n.KeyErrorRecheckUnavailable, store.ErrInvalidTransition: i18n.KeyErrorInvalidTransition,
	pipeline.ErrGroupInconsistent: i18n.KeyErrorGroupInconsistent, pipeline.ErrClientReadOnly: i18n.KeyErrorClientReadOnly,
	pipeline.ErrPlacementChanged: i18n.KeyErrorPlacementChanged, dedupe.ErrTorrentRootOverlapsLibrary: i18n.KeyErrorTorrentRootOverlaps,
	repair.ErrOutsideRoots: i18n.KeyErrorOutsideRoots, store.ErrProposalNotFound: i18n.KeyErrorProposalNotFound,
	ErrGroupNotFound: i18n.KeyErrorGroupNotFound, ErrNotConfigured: i18n.KeyErrorNotConfigured,
	pipeline.ErrStageRunning: i18n.KeyErrorStageRunning, ErrInspectionBusy: i18n.KeyErrorInspectionBusy,
	ErrSourceNotFound: i18n.KeyErrorSourceNotFound, ErrSourceNotTestable: i18n.KeyErrorSourceNotTestable,
	ErrSourceTestRunning: i18n.KeyErrorSourceTestRunning, ErrVolumeNotFound: i18n.KeyErrorVolumeNotFound,
	ErrVolumeProbeRunning: i18n.KeyErrorVolumeProbeRunning,
}

// MessageOf donne le message à rendre pour err : la clé de l'erreur typée reconnue (avec le volume d'un refus de
// Force distant), sinon error.internal. Le texte brut
// d'une erreur non typée n'en fait jamais partie (il reste pour le journal, masqué).
func MessageOf(err error) i18n.Message {
	_, typed := Classify(err)
	key, known := messageKeys[typed]
	if !known {
		return i18n.Message{Key: i18n.KeyErrorInternal}
	}
	message := i18n.Message{Key: key}
	var refusal *pipeline.ForceRefusal
	if errors.As(err, &refusal) {
		message.Params = map[string]string{"volume": refusal.Volume}
	}
	return message
}
