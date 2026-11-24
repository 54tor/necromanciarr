package app

import (
	"errors"

	"necromanciarr/internal/config"
	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/repair"
	"necromanciarr/internal/store"
)

// Codes d'erreur stables partagés par la sortie JSON de la CLI et l'API web ({"error": {"code", "message"}}).
const (
	ErrorCodeUsage         = "usage"
	ErrorCodeStale         = "stale_proposal"
	ErrorCodeRefused       = "refused"
	ErrorCodeNotFound      = "not_found"
	ErrorCodeNotConfigured = "not_configured"
	ErrorCodeStageRunning  = "stage_running"
	ErrorCodeFailure       = "failure"
)

// refusals sont les refus typés des règles métier : la demande est bien formée, l'action ne l'est pas.
var refusals = []error{
	ErrConflictNeedsChoose, ErrPartialPackNeedsForce, ErrNotATorrentProposal, ErrCandidateAlreadyAccepted,
	ErrOneTorrentPerFile, ErrNotFull, ErrNotForceable, ErrNoPrimary, ErrForceNotAllowed,
	ErrNotAccepted, ErrNotActionable, ErrWrongProposalKind, ErrDirectoryInLibrary,
	store.ErrInvalidTransition, pipeline.ErrGroupInconsistent, pipeline.ErrClientReadOnly,
	dedupe.ErrTorrentRootOverlapsLibrary, repair.ErrOutsideRoots,
}

// classes associe chaque code aux erreurs typées qu'il couvre, dans l'ordre de priorité du classement.
var classes = []struct {
	code   string
	errors []error
}{
	{ErrorCodeUsage, []error{config.ErrUsage, ErrUnknownCheck}},
	{ErrorCodeStale, []error{ErrStaleProposal}},
	{ErrorCodeRefused, refusals},
	{ErrorCodeNotFound, []error{store.ErrProposalNotFound, ErrGroupNotFound}},
	{ErrorCodeNotConfigured, []error{ErrNotConfigured}},
	{ErrorCodeStageRunning, []error{pipeline.ErrStageRunning}},
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
