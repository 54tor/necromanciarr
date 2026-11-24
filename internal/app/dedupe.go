package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/qbit"
	"necromanciarr/internal/store"
)

// DedupeDetails est le détail JSON d'une proposition dedupe : remplacer TorrentPath par un lien de sorte Kind vers
// LibraryPath. Size, SavedBytes et BytesToRead (lecture estimée de la preuve, annoncée avant acceptation) sont en
// octets. Actionable est faux quand aucun remplacement n'est possible : Reason en donne la cause, ou Ambiguous liste
// les fichiers de la bibliothèque entre lesquels aucun choix n'est fait. Les empreintes, dates et l'inode servent à
// refuser à l'application un fichier changé depuis le plan.
type DedupeDetails struct {
	TorrentPath        string    `json:"torrent_path"`
	LibraryPath        string    `json:"library_path,omitempty"`
	Kind               string    `json:"kind,omitempty"`
	Size               int64     `json:"size"`
	SavedBytes         int64     `json:"saved_bytes"`
	BytesToRead        int64     `json:"bytes_to_read"`
	Method             string    `json:"method"`
	HashAlgorithm      string    `json:"hash_algorithm,omitempty"`
	Warnings           []string  `json:"warnings,omitempty"`
	Ambiguous          []string  `json:"ambiguous,omitempty"`
	Reason             string    `json:"reason,omitempty"`
	Label              string    `json:"label,omitempty"`
	Actionable         bool      `json:"actionable"`
	Fingerprint        string    `json:"fingerprint"`
	LibraryFingerprint string    `json:"library_fingerprint,omitempty"`
	TorrentModTime     time.Time `json:"torrent_mtime"`
	LibraryModTime     time.Time `json:"library_mtime,omitzero"`
	LibraryDevice      uint64    `json:"library_device,omitempty"`
	LibraryInode       uint64    `json:"library_inode,omitempty"`
	InodeReliable      bool      `json:"inode_reliable"`
}

// Motifs stables d'une proposition dedupe sans remplacement possible (DedupeDetails.Reason, jamais traduits).
const (
	// DedupeReasonIsolatedCopy : le fichier côté torrent est une copie indépendante posée par Force (ou un reflink) ; le
	// relier à la bibliothèque déferait l'isolation qui protège celle-ci.
	DedupeReasonIsolatedCopy = "isolated_copy"
	// DedupeReasonHashUnavailable : la vérification par empreinte est exigée et le stockage n'en fournit pas.
	DedupeReasonHashUnavailable = "hash_unavailable"
	// DedupeReasonNoKind : aucune sorte de lien n'est demandée dans la configuration.
	DedupeReasonNoKind = "no_link_kind"
	// DedupeReasonCrossDevice : le torrent et la bibliothèque sont sur deux volumes.
	DedupeReasonCrossDevice = "cross_device"
	// DedupeReasonKindUnsupported : aucune sorte demandée n'est prise en charge par ce dossier.
	DedupeReasonKindUnsupported = "link_kind_unsupported"
	// DedupeReasonProbeFailed : la sonde du dossier torrent a échoué, ou son échec n'a pas de motif plus précis.
	DedupeReasonProbeFailed = "link_probe_failed"
)

// dedupeReasonCode ramène la cause d'un plan sans remplacement à un motif stable : l'ordre est celui de la précision
// (empreinte, configuration, volume, prise en charge), tout le reste est un échec de sonde.
func dedupeReasonCode(cause error) string {
	switch {
	case errors.Is(cause, dedupe.ErrHashUnavailable):
		return DedupeReasonHashUnavailable
	case errors.Is(cause, linker.ErrNoKind):
		return DedupeReasonNoKind
	case errors.Is(cause, linker.ErrCrossDevice):
		return DedupeReasonCrossDevice
	case errors.Is(cause, linker.ErrUnsupported):
		return DedupeReasonKindUnsupported
	}
	return DedupeReasonProbeFailed
}

// storedWarningKeys range les avertissements d'un plan sous leur clé : les messages de dedupe n'ont pas de paramètre.
func storedWarningKeys(messages []i18n.Message) []string {
	keys := make([]string, 0, len(messages))
	for _, message := range messages {
		keys = append(keys, string(message.Key))
	}
	return keys
}

// DedupeProposal est une proposition dedupe telle que la CLI et le web l'affichent.
type DedupeProposal struct {
	ID       int64                  `json:"id"`
	Decision store.ProposalDecision `json:"decision"`
	Warnings []Warning              `json:"warnings"`
	Details  DedupeDetails          `json:"details"`
}

func (details DedupeDetails) identity() (key, signature string) {
	key = details.TorrentPath + "\x00" + details.LibraryPath
	signature = fmt.Sprint(details.Size, details.TorrentModTime.UnixNano(), details.LibraryModTime.UnixNano(), details.Fingerprint,
		details.LibraryFingerprint, details.Kind, details.LibraryInode, strings.Join(details.Ambiguous, "\x00"), details.Actionable,
		slices.Contains(details.Warnings, WarningNoClientRecheck) || slices.Contains(details.Warnings, legacyNoClientRecheck))
	return key, signature
}

// labelled renvoie le détail avec le libellé traduit du motif ; un motif qui n'est pas un code connu reste tel quel.
func (details DedupeDetails) labelled(translator i18n.Translator) DedupeDetails {
	if details.Reason != "" {
		details.Label = translator.ReasonLabel(details.Reason)
	}
	return details
}

func dedupeActionable(details DedupeDetails) bool { return details.Actionable }

func dedupeDetailsOf(replacement dedupe.Replacement) DedupeDetails {
	details := DedupeDetails{
		TorrentPath: replacement.TorrentPath, LibraryPath: replacement.LibraryPath, Size: replacement.Size,
		SavedBytes: replacement.SavedBytes, BytesToRead: replacement.BytesToRead, Method: string(replacement.Method),
		HashAlgorithm: string(replacement.HashAlgorithm), Warnings: storedWarningKeys(replacement.Warnings), Ambiguous: replacement.Ambiguous,
		Actionable: replacement.Kind != 0 && len(replacement.Ambiguous) == 0, Fingerprint: replacement.Fingerprint,
		LibraryFingerprint: replacement.LibraryFingerprint, TorrentModTime: replacement.TorrentModTime,
		LibraryModTime: replacement.LibraryModTime, LibraryDevice: replacement.LibraryDevice,
		LibraryInode: replacement.LibraryInode, InodeReliable: replacement.InodeReliable,
	}
	if replacement.Kind != 0 {
		details.Kind = replacement.Kind.String()
	}
	if replacement.Reason != nil {
		details.Reason = dedupeReasonCode(replacement.Reason)
	}
	return details
}

func (details DedupeDetails) replacement() dedupe.Replacement {
	return dedupe.Replacement{
		TorrentPath: details.TorrentPath, LibraryPath: details.LibraryPath, Size: details.Size, Kind: linkKindByName[details.Kind],
		SavedBytes: details.SavedBytes, Ambiguous: details.Ambiguous, Warnings: messagesOfCodes(details.Warnings), BytesToRead: details.BytesToRead,
		Method: dedupe.Method(details.Method), HashAlgorithm: hashsource.Algorithm(details.HashAlgorithm),
		Fingerprint: details.Fingerprint, LibraryFingerprint: details.LibraryFingerprint, TorrentModTime: details.TorrentModTime,
		LibraryModTime: details.LibraryModTime, LibraryDevice: details.LibraryDevice, LibraryInode: details.LibraryInode,
		InodeReliable: details.InodeReliable,
	}
}

func dedupeWarnings(details DedupeDetails) []string { return details.Warnings }

// neverOutOfScope : un plan dedupe parcourt toujours toutes les racines configurées.
func neverOutOfScope(DedupeDetails) bool { return false }

func (svc *service) DedupePlan(ctx context.Context) ([]DedupeProposal, error) {
	var proposals []DedupeProposal
	err := svc.locked(ctx, lockNameDedupe, func(ctx context.Context) error {
		replacements, err := svc.planDedupe(ctx)
		if err != nil {
			return err
		}
		isolated, err := svc.isolatedCopies(ctx)
		if err != nil {
			return err
		}
		entries := make([]DedupeDetails, 0, len(replacements))
		for _, replacement := range replacements {
			details := dedupeDetailsOf(replacement)
			if isolated[details.TorrentPath] {
				details.Actionable, details.Reason = false, DedupeReasonIsolatedCopy
			}
			if svc.linksWithoutRecheck(details) {
				details.Warnings = append(details.Warnings, WarningNoClientRecheck)
			}
			entries = append(entries, details)
		}
		persisted, err := persistMaintenance(ctx, svc, store.ProposalDedupe, entries, dedupeWarnings, neverOutOfScope)
		for _, stored := range persisted {
			proposals = append(proposals, DedupeProposal{
				ID: stored.proposal.ID, Decision: stored.proposal.Decision, Warnings: warningViews(svc.deps.Translator, stored.proposal.Warnings), Details: stored.details.labelled(svc.deps.Translator),
			})
		}
		return err
	})
	return proposals, err
}

// isolatedCopies renvoie les chemins enregistrés comme copie ou reflink : dedupe ne les remplace jamais par un lien.
func (svc *service) isolatedCopies(ctx context.Context) (map[string]bool, error) {
	links, err := svc.deps.Store.ListLinks(ctx, "")
	if err != nil {
		return nil, err
	}
	isolated := map[string]bool{}
	for _, link := range links {
		if link.Kind == linker.Copy.String() || link.Kind == linker.Reflink.String() {
			isolated[link.LinkPath] = true
		}
	}
	return isolated, nil
}

// linksWithoutRecheck : un lien dur ou symbolique fait pointer le torrent sur le fichier de la bibliothèque ; sans
// recontrôle du client, rien ne garantit qu'un torrent incomplet n'y écrira pas.
func (svc *service) linksWithoutRecheck(details DedupeDetails) bool {
	sharesLibraryFile := details.Kind == linker.Hardlink.String() || details.Kind == linker.Symlink.String()
	recheckPlanned := svc.canRecheck() && svc.deps.Config.Dedupe.RecheckAfter
	return details.Actionable && sharesLibraryFile && !recheckPlanned
}

// planDedupe refuse des racines torrent qui recouvrent la bibliothèque avant toute écriture, reprend les entrées
// temporaires d'un Apply interrompu (Recover), puis cherche les remplacements sur l'inventaire.
func (svc *service) planDedupe(ctx context.Context) ([]dedupe.Replacement, error) {
	settings := svc.deps.Config
	torrentRoots := settings.Dedupe.TorrentRoots
	if len(torrentRoots) == 0 {
		return nil, fmt.Errorf("%w: dedupe.torrentRoots", ErrNotConfigured)
	}
	if err := checkOutsideLibrary(torrentRoots, settings.LibraryRoots, dedupe.ErrTorrentRootOverlapsLibrary); err != nil {
		return nil, err
	}
	if _, err := dedupe.Recover(ctx, torrentRoots); err != nil {
		return nil, err
	}
	library, err := svc.deps.Store.ListMediaFiles(ctx)
	if err != nil {
		return nil, err
	}
	return dedupe.Plan(ctx, torrentRoots, library, svc.dedupeOptions(ctx))
}

// dedupeOptions demande au stockage les empreintes que ses montages fournissent, d'après le cache des capacités
// (une sonde par montage, la première fois seulement) ; sans réponse, dedupe garde MD5.
func (svc *service) dedupeOptions(ctx context.Context) dedupe.Options {
	settings := svc.deps.Config.Dedupe
	kinds := make([]linker.Kind, 0, len(settings.Kinds))
	for _, kindName := range settings.Kinds {
		kinds = append(kinds, linkKindByName[kindName])
	}
	algorithms, _ := svc.deps.HashSources.CachedHashAlgorithms(ctx, svc.deps.Store)
	return dedupe.Options{
		Kinds: kinds, RelativeSymlinks: settings.RelativeSymlinks, MinFileAge: settings.MinFileAge,
		RecheckAfter: settings.RecheckAfter, Verify: dedupe.Verification(settings.Verify), TorrentRoots: settings.TorrentRoots,
		Limiter: svc.deps.Stages.ReadLimiter, Fingerprints: svc.deps.Store, Recorder: svc.deps.Store,
		Backend: svc.deps.HashSources, HashAlgorithms: algorithms,
	}
}

func (svc *service) DedupeAccept(ctx context.Context, proposalID int64, options ...DecisionOption) (MaintenanceResult, error) {
	result := MaintenanceResult{ProposalID: proposalID, DryRun: slices.Contains(options, DryRun)}
	err := svc.locked(ctx, lockNameDedupe, func(ctx context.Context) error {
		details, err := loadAcceptedMaintenance(ctx, svc, proposalID, store.ProposalDedupe, dedupeActionable, options)
		if err != nil {
			return err
		}
		return svc.applyDedupe(ctx, details, &result)
	})
	return result, err
}

func (svc *service) applyDedupe(ctx context.Context, details DedupeDetails, result *MaintenanceResult) error {
	if err := checkOutsideLibrary(svc.deps.Config.Dedupe.TorrentRoots, svc.deps.Config.LibraryRoots, dedupe.ErrTorrentRootOverlapsLibrary); err != nil {
		return err
	}
	isolated, err := svc.isolatedCopies(ctx)
	if err != nil {
		return err
	}
	if isolated[details.TorrentPath] {
		return fmt.Errorf("%w: %s", ErrNotActionable, DedupeReasonIsolatedCopy)
	}
	options := svc.dedupeOptions(ctx)
	options.DryRun = result.DryRun
	if svc.canRecheck() {
		options.Rechecker = svc.clientView()
	}
	applied, applyErr := dedupe.Apply(ctx, []dedupe.Replacement{details.replacement()}, options)
	if len(applied.Outcomes) == 0 {
		return applyErr
	}
	outcome := applied.Outcomes[0]
	result.LinkTarget, result.BytesRead = outcome.LinkTarget, outcome.BytesRead
	if result.DryRun {
		return outcome.Err
	}
	if outcome.Done {
		*result = result.withWarnings(svc.deps.Translator, outcomeWarnings(outcome.Err, outcome.RecheckErr))
	}
	finishCtx, cancel := bookkeeping(ctx)
	defer cancel()
	return svc.finishMaintenance(finishCtx, result, outcome.Done, outcome.Err, dedupe.ErrChangedSincePlan)
}

// outcomeWarnings : enregistrement manqué après remplacement, et sort du recontrôle ; un torrent incomplet après
// recontrôle reste en pause (le client le garde ainsi), jamais repris ici.
func outcomeWarnings(recordErr, recheckErr error) []i18n.Message {
	var warnings []i18n.Message
	if recordErr != nil {
		warnings = append(warnings, i18n.Message{Key: i18n.KeyWarningLinkNotRecorded})
	}
	switch {
	case errors.Is(recheckErr, qbit.ErrIncompleteAfterRecheck):
		warnings = append(warnings, i18n.Message{Key: i18n.KeyWarningPausedIncompleteAfterRecheck})
	case recheckErr != nil:
		warnings = append(warnings, i18n.Message{Key: i18n.KeyWarningRecheckFailed})
	}
	return warnings
}
