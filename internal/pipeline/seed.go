package pipeline

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/qbit"
	"necromanciarr/internal/store"
)

// Erreurs typées de Seed.
var (
	ErrNoTorrentClient = errors.New("pipeline: no torrent client configured")
	// ErrClientReadOnly : le client est déclaré readonly, aucun torrent n'est ajouté et l'étape s'arrête.
	ErrClientReadOnly = errors.New("pipeline: torrent client is read-only")
)

// Raisons propres à Seed.
const (
	ReasonNotLinked              = "not_linked"
	ReasonClientReadOnly         = "client_read_only"
	ReasonAddFailed              = "add_failed"
	ReasonRecheckFailed          = "recheck_failed"
	ReasonResumeFailed           = "resume_failed"
	ReasonIncompleteAfterRecheck = "incomplete_after_recheck"
	ReasonPausedIncomplete       = "paused_incomplete"
	ReasonCrossSeedNotifyFailed  = "crossseed_notify_failed"
	ReasonLinkChanged            = "link_changed"
	ReasonAnnexChanged           = "annex_changed"
	ReasonAlreadyInClient        = "already_in_client"
	ReasonClientPathInvalid      = "client_path_invalid"
	ReasonPauseFailed            = "pause_failed"
	ReasonNotPausedAfterAdd      = "not_paused_after_add"
	// ReasonIdentityUnproven : un lien du torrent n'est pas prouvé par l'identité du fichier (lien distant) ;
	// skip_checking est refusé, le client recontrôle et le torrent reste arrêté jusqu'à 100 %.
	ReasonIdentityUnproven = "identity_unproven"
	// ReasonClientPathUnmapped : aucune règle pathMappings ne traduit le dossier de liens distant vers la vue du client.
	ReasonClientPathUnmapped = "client_path_unmapped"
	// ReasonRemoteUnavailable : le volume distant ne répond pas ; la proposition reste en l'état, reprise à la relance.
	ReasonRemoteUnavailable = "remote_unavailable"
)

// Avertissements consignés sur la proposition (message fixe, sans URL ni secret).
var (
	warningIncompleteAfterRecheck = i18n.Message{Key: i18n.KeyWarningIncompleteAfterRecheck}
	warningCrossSeedNotifyFailed  = i18n.Message{Key: i18n.KeyWarningCrossSeedNotifyFailed}
	warningSourcesChanged         = i18n.Message{Key: i18n.KeyWarningSourcesChanged}
)

// defaultSeedRecheckTimeout couvre le recontrôle d'un gros torrent sur disque lent ; au-delà, il reste en pause.
const defaultSeedRecheckTimeout = time.Hour

// runSeed ajoute au client, une proposition liée à la fois, chaque torrent toujours arrêté ; seul un torrent complet
// (notre vérification à 100 %, confirmée par le client) est repris. Un torrent incomplet reste en pause : le client
// réécrirait sinon des pièces à cheval sur un fichier lié de la bibliothèque.
func runSeed(ctx context.Context, deps Deps) (Result, error) {
	var result Result
	result.Warnings = append(result.Warnings, deps.CrossSeedWarnings...)
	if deps.Client == nil {
		return result, ErrNoTorrentClient
	}
	work, err := loadAcceptedWork(ctx, deps)
	if err != nil {
		return result, err
	}
	if err := work.checkGroups(); err != nil {
		return result, err
	}
	run := seedRun{deps: deps, result: &result, discovered: &[]string{}}
	for _, proposal := range work.proposals {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		candidate, reason := work.eligibility(proposal)
		progress := work.progress[proposal.ID]
		if reason == "" && progress.Step != store.StepLinked {
			reason = ReasonNotLinked
		}
		if reason != "" {
			result.Skipped++
			result.count(reason, 1)
			continue
		}
		if err := run.seedProposal(ctx, proposal, candidate, progress.LinkRoot); err != nil {
			return result, run.redact(err)
		}
	}
	return result, nil
}

// seedRun porte une exécution de Seed ; discovered retient les valeurs de passkey lues dans les .torrent d'indexeur de
// l'étape, qui masquent toute erreur sortante (redact).
type seedRun struct {
	deps       Deps
	result     *Result
	discovered *[]string
}

// redact masque, dans l'erreur qui arrête l'étape, les passkeys découvertes pendant l'étape, sous toutes les formes de
// config.Config.WithDiscoveredSecrets ; l'application masque en plus les secrets connus de la configuration.
func (run seedRun) redact(err error) error {
	if len(*run.discovered) == 0 {
		return err
	}
	secrets := config.Config{}.WithDiscoveredSecrets(*run.discovered...)
	return redactedError{text: config.RedactText(err.Error(), secrets...), cause: err}
}

// seedProposal ne renvoie une erreur que pour arrêter l'étape ; un refus (refusal) passe la proposition à failed.
// Une coupure du volume distant laisse la proposition liée, reprise à la relance.
func (run seedRun) seedProposal(ctx context.Context, proposal store.Proposal, candidate store.Candidate, linkRoot string) error {
	err := run.seedChecked(ctx, proposal, candidate, linkRoot)
	if isRemoteUnavailable(err) {
		run.result.Skipped++
		run.result.count(ReasonRemoteUnavailable, 1)
		return nil
	}
	var refused refusal
	if errors.As(err, &refused) {
		return failProposal(ctx, run.deps, run.result, proposal.ID, string(refused))
	}
	return err
}

// seedChecked revérifie tout juste avant l'ajout : sources inchangées (sinon candidat stale), liens et annexes du
// dossier du torrent intacts (sinon échec et étape effacée, Link refera le dossier). Un torrent déjà dans le client
// n'est repris en main que s'il pointe sur notre dossier.
func (run seedRun) seedChecked(ctx context.Context, proposal store.Proposal, candidate store.Candidate, linkRoot string) error {
	// Défense : Link a déjà refusé une proposition sans annonce ou sans clé ; rien n'est relu ni ajouté.
	if reason := announceRefusal(run.deps, candidate); reason != "" {
		return refusal(reason)
	}
	proven, reason, err := loadProven(ctx, run.deps, proposal, candidate)
	switch {
	case err != nil:
		return err
	case reason == ReasonChangedFile:
		return run.markSourcesChanged(ctx, proposal.ID, candidate.ID)
	case reason != "":
		return refusal(reason)
	}
	proof, damage, err := run.checkPlacement(ctx, proven, linkRoot)
	if err != nil {
		return err
	}
	if damage != "" {
		if err := run.deps.Store.ClearProposalStep(ctx, proposal.ID, run.deps.now()); err != nil {
			return err
		}
		return refusal(damage)
	}
	savePath, pathRefusal := run.clientPath(linkRoot)
	if pathRefusal != "" {
		return refusal(pathRefusal)
	}
	complete, err := run.verifiedComplete(ctx, proposal, candidate.ID)
	if err != nil {
		return err
	}
	existing, present, err := run.findTorrent(ctx, candidate.Infohash)
	if err != nil {
		return refusal(ReasonAddFailed)
	}
	if present {
		return run.takeOverExisting(ctx, proposal.ID, candidate.Infohash, existing, savePath, linkRoot, complete)
	}
	skipAllowed := complete && run.deps.SkipRecheck
	skipChecking := skipAllowed && proof.certain
	if skipAllowed && proof.identityUnproven {
		run.result.count(ReasonIdentityUnproven, 1)
	}
	if err := run.addStopped(ctx, candidate, savePath, skipChecking); err != nil {
		return err
	}
	if !complete {
		if err := run.ensurePausedAfterAdd(ctx, candidate.Infohash); err != nil {
			return err
		}
		run.result.count(ReasonPausedIncomplete, 1)
		return run.finish(ctx, proposal.ID, store.StepSeededPausedIncomplete, linkRoot)
	}
	return run.startComplete(ctx, proposal.ID, candidate.Infohash, linkRoot, skipChecking)
}

// markSourcesChanged : une source a changé entre Link et Seed ; rien n'est ajouté et le candidat passe à stale (le
// store clôt alors la proposition, l'avertissement en garde la raison).
func (run seedRun) markSourcesChanged(ctx context.Context, proposalID, candidateID int64) error {
	run.result.Skipped++
	run.result.count(ReasonChangedFile, 1)
	if err := run.deps.Store.AddProposalWarning(ctx, proposalID, warningSourcesChanged); err != nil {
		return err
	}
	return run.deps.Store.SetCandidateState(ctx, candidateID, store.CandidateStale, run.deps.now())
}

// placementProof : certain dit que chaque lien est prouvé par l'identité du fichier (I4) ; identityUnproven, qu'un
// lien distant (ou enregistré comme tel par une exécution précédente) l'empêche, quoi que dise le volume.
type placementProof struct {
	certain          bool
	identityUnproven bool
}

// checkPlacement contrôle chaque lien (même fichier que sa source, ou même taille et même empreinte pour un reflink) et
// chaque annexe (taille et sha256), par le registre. La preuve n'est certaine que si chaque lien l'est par l'identité du
// fichier sur le disque local, et qu'aucun n'est enregistré comme non prouvé : le client doit sinon recontrôler.
func (run seedRun) checkPlacement(ctx context.Context, proven provenTorrent, linkRoot string) (placementProof, string, error) {
	root, rootErr := locationOfStoredPath(linkRoot)
	if rootKnown := rootErr == nil; !rootKnown {
		return placementProof{}, ReasonLinkChanged, nil
	}
	ops, planErr := planOps(proven, root)
	planned := planErr == nil
	if !planned {
		return placementProof{}, ReasonLinkChanged, nil
	}
	readFS, _, err := readRegistryOrLocal(run.deps.Registry).Read(root)
	if err != nil {
		return placementProof{}, "", err
	}
	recordedUnproven, err := run.recordedUnproven(ctx)
	if err != nil {
		return placementProof{}, "", err
	}
	proof := placementProof{certain: true}
	for _, operation := range ops {
		match, err := entryMatches(ctx, run.deps, readFS, operation.Destination, operation.Source)
		if err != nil {
			return placementProof{}, "", err
		}
		if !match.matches {
			return placementProof{}, ReasonLinkChanged, nil
		}
		isUnproven := root.Volume != "" || recordedUnproven[storedIn(root.Volume, operation.Destination)]
		proof.identityUnproven = proof.identityUnproven || isUnproven
		proof.certain = proof.certain && match.identityProven && !isUnproven
	}
	for _, annex := range proven.annexes {
		path, pathErr := annexPath(proven.torrent, root, annex)
		if pathKnown := pathErr == nil; !pathKnown {
			return placementProof{}, ReasonAnnexChanged, nil
		}
		matches, err := annexMatches(readFS, path, annex)
		if err != nil {
			return placementProof{}, "", err
		}
		if !matches {
			return placementProof{}, ReasonAnnexChanged, nil
		}
	}
	return proof, "", nil
}

// recordedUnproven rend les liens enregistrés dont l'identité n'est pas prouvée : l'état survit d'une exécution à
// l'autre, même si le volume rendait plus tard une identité.
func (run seedRun) recordedUnproven(ctx context.Context) (map[string]bool, error) {
	links, err := run.deps.Store.ListLinks(ctx, "")
	if err != nil {
		return nil, err
	}
	unproven := map[string]bool{}
	for _, link := range links {
		if link.IdentityUnproven() {
			unproven[link.LinkPath] = true
		}
	}
	return unproven, nil
}

// verifiedComplete : seul un torrent complet dont NOTRE vérification a couvert toutes les pièces peut être repris ;
// l'import skipRecheck de cross-seed ne l'emporte jamais sur ce principe.
func (run seedRun) verifiedComplete(ctx context.Context, proposal store.Proposal, candidateID int64) (bool, error) {
	isCompleteKind := proposal.Kind == store.ProposalFull || proposal.Kind == store.ProposalCrossSeed
	if !isCompleteKind {
		return false, nil
	}
	verification, found, err := run.deps.Store.LoadVerification(ctx, candidateID)
	if err != nil {
		return false, err
	}
	return found && verification.TotalPieces > 0 && verification.VerifiedPieces == verification.TotalPieces, nil
}

// findTorrent interroge le client par hash : la liste entière d'un gros client dépasserait le plafond de réponse.
func (run seedRun) findTorrent(ctx context.Context, infohash string) (qbit.TorrentInfo, bool, error) {
	return run.deps.Client.FindTorrent(ctx, infohash)
}

// takeOverExisting reprend un torrent déjà dans le client (reprise après un arrêt) seulement s'il pointe sur notre
// dossier ; sinon il appartient à quelqu'un d'autre et rien n'est touché. Incomplet, il est mis en pause ; complet, la
// garde le recontrôle et ne le reprend qu'à 100 %.
func (run seedRun) takeOverExisting(ctx context.Context, proposalID int64, infohash string, existing qbit.TorrentInfo, savePath, linkRoot string, complete bool) error {
	if !sameClientPath(existing.SavePath, savePath) {
		return refusal(ReasonAlreadyInClient)
	}
	if !complete {
		if err := run.deps.Client.Pause(ctx, infohash); err != nil {
			return refusal(ReasonPauseFailed)
		}
		run.result.count(ReasonPausedIncomplete, 1)
		return run.finish(ctx, proposalID, store.StepSeededPausedIncomplete, linkRoot)
	}
	return run.guarded(ctx, proposalID, infohash, linkRoot)
}

// pauseCheckAttempts et pauseCheckInterval : qBittorrent ajoute le torrent de façon asynchrone ; il peut n'apparaître
// qu'après quelques centaines de millisecondes.
const (
	pauseCheckAttempts = 10
	pauseCheckInterval = 200 * time.Millisecond
	// pauseRetries : une pause, puis une seconde si le client a démarré le torrent malgré tout.
	pauseRetries = 2
	// removeTimeout borne le retrait d'un torrent qui refuse la pause, fait même si l'appelant annule.
	removeTimeout = 30 * time.Second
)

// ensurePausedAfterAdd redemande la pause d'un torrent incomplet tout juste ajouté, puis vérifie que le client le
// montre arrêté (ou en contrôle, qui finit arrêté) : un client qui l'aurait démarré quand même écrirait dans les
// fichiers liés. Toujours en marche après une seconde pause, il est retiré du client, données gardées.
func (run seedRun) ensurePausedAfterAdd(ctx context.Context, infohash string) error {
	for pauseAttempt := range pauseRetries {
		if err := run.deps.Client.Pause(ctx, infohash); err != nil {
			return refusal(ReasonPauseFailed)
		}
		stopped, err := run.waitStopped(ctx, infohash)
		if err != nil || stopped {
			return err
		}
		if pauseAttempt == pauseRetries-1 {
			break
		}
	}
	removeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), removeTimeout)
	defer cancel()
	_ = run.deps.Client.RemoveKeepingData(removeCtx, infohash)
	return refusal(ReasonNotPausedAfterAdd)
}

// waitStopped relit le torrent par hash jusqu'à le voir (ajout asynchrone) ; stopped dit s'il est arrêté ou en
// contrôle. Introuvable ou illisible : refus.
func (run seedRun) waitStopped(ctx context.Context, infohash string) (bool, error) {
	for attempt := range pauseCheckAttempts {
		torrent, present, err := run.deps.Client.FindTorrent(ctx, infohash)
		if err != nil {
			return false, refusal(ReasonNotPausedAfterAdd)
		}
		if present {
			return isStoppedState(torrent.State), nil
		}
		if attempt < pauseCheckAttempts-1 {
			if err := sleepContext(ctx, pauseCheckInterval); err != nil {
				return false, err
			}
		}
	}
	return false, refusal(ReasonNotPausedAfterAdd)
}

func isStoppedState(state string) bool {
	isStopped := strings.HasPrefix(state, "paused") || strings.HasPrefix(state, "stopped")
	isChecking := strings.HasPrefix(state, "checking") || state == "queuedForChecking"
	return isStopped || isChecking
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// addStopped ajoute le torrent arrêté au chemin que voit le client ; un client readonly arrête l'étape. Le .torrent
// remis, annonce comprise, n'existe qu'en mémoire le temps de l'appel ; l'erreur du client n'est jamais relue.
func (run seedRun) addStopped(ctx context.Context, candidate store.Candidate, savePath string, skipChecking bool) error {
	injected, err := run.announcedTorrent(ctx, candidate)
	if err != nil {
		return err
	}
	// Au mieux : Go peut avoir copié ces octets ailleurs (pile, ramasse-miettes, tampon du client HTTP).
	defer clear(injected)
	options := qbit.AddOptions{Category: run.deps.Category, Tags: run.deps.Tags, Start: false, SkipChecking: skipChecking}
	err = run.deps.Client.Add(ctx, injected, savePath, options)
	switch {
	case errors.Is(err, qbit.ErrReadOnly):
		run.result.count(ReasonClientReadOnly, 1)
		return ErrClientReadOnly
	case err != nil:
		return refusal(ReasonAddFailed)
	}
	return nil
}

// refusal est un refus nommé par sa raison : la proposition passe à failed et l'étape continue.
type refusal string

func (reason refusal) Error() string { return "pipeline: refused: " + string(reason) }

// clientPath traduit le dossier du torrent vers la vue du client et rend, sinon, la raison du refus. En local, sans
// règle qui le couvre (linker.ErrNoRule), le client voit le même chemin ; un dossier distant exige une règle
// (client_path_unmapped), « même chemin » n'ayant aucun sens entre deux machines. Un chemin Windows qui contient un
// caractère interdit par Windows est refusé.
func (run seedRun) clientPath(linkRoot string) (string, string) {
	if strings.HasPrefix(linkRoot, remoteLocationPrefix) {
		return run.remoteClientPath(linkRoot)
	}
	translated := linkRoot
	if run.deps.ClientMapper != nil {
		if mapped, err := run.deps.ClientMapper.Translate(linkRoot); err == nil {
			translated = mapped
		}
	}
	return windowsChecked(translated)
}

// remoteLocationPrefix ouvre une adresse distante enregistrée ; la règle doit la traduire hors de cette forme.
const remoteLocationPrefix = "sftp:"

func (run seedRun) remoteClientPath(linkRoot string) (string, string) {
	if run.deps.ClientMapper == nil {
		return "", ReasonClientPathUnmapped
	}
	mapped, err := run.deps.ClientMapper.Translate(linkRoot)
	isMapped := err == nil && mapped != "" && !strings.HasPrefix(mapped, remoteLocationPrefix)
	if !isMapped {
		return "", ReasonClientPathUnmapped
	}
	return windowsChecked(mapped)
}

func windowsChecked(clientPath string) (string, string) {
	if hasWindowsInvalidCharacters(clientPath) {
		return "", ReasonClientPathInvalid
	}
	return clientPath, ""
}

// windowsInvalidCharacters sont interdits dans un nom de fichier Windows (le « : » du lecteur mis à part).
const windowsInvalidCharacters = `:?*"<>|`

func hasWindowsInvalidCharacters(clientPath string) bool {
	isDrivePath := len(clientPath) >= 2 && clientPath[1] == ':'
	isUNCPath := strings.HasPrefix(clientPath, `\\`)
	if !isDrivePath && !isUNCPath {
		return false
	}
	return strings.ContainsAny(clientPath[2:], windowsInvalidCharacters)
}

// sameClientPath compare deux chemins vus par le client, nettoyés, en ignorant la casse pour un chemin Windows.
func sameClientPath(left, right string) bool {
	normalize := func(clientPath string) string {
		return strings.TrimSuffix(path.Clean(strings.ReplaceAll(clientPath, `\`, "/")), "/")
	}
	isWindows := (len(right) >= 2 && right[1] == ':') || strings.HasPrefix(right, `\\`)
	if isWindows {
		return strings.EqualFold(normalize(left), normalize(right))
	}
	return normalize(left) == normalize(right)
}

// startComplete reprend un torrent complet. Après skip_checking, qBittorrent affiche 100 % par construction : ce
// contrôle ne prouve que l'ajout, la preuve du contenu est la revérification de checkPlacement juste avant. Sans
// skip_checking (ou si l'avancement n'est pas à 1), la garde recontrôle et ne reprend qu'à 100 %.
func (run seedRun) startComplete(ctx context.Context, proposalID int64, infohash, linkRoot string, skipChecking bool) error {
	if skipChecking {
		existing, present, err := run.findTorrent(ctx, infohash)
		if err == nil && present && existing.Progress == 1 {
			if err := run.deps.Client.Resume(ctx, infohash); err != nil {
				return refusal(ReasonResumeFailed)
			}
			return run.seeded(ctx, proposalID, infohash, linkRoot)
		}
	}
	return run.guarded(ctx, proposalID, infohash, linkRoot)
}

// guarded recontrôle (le torrent est mis en pause avant) et ne reprend qu'à 100 % ; incomplet, il reste en pause.
func (run seedRun) guarded(ctx context.Context, proposalID int64, infohash, linkRoot string) error {
	timeout := run.deps.RecheckTimeout
	if timeout <= 0 {
		timeout = defaultSeedRecheckTimeout
	}
	guard, err := run.deps.Client.RecheckAndGuard(ctx, infohash, timeout)
	if err != nil {
		return refusal(ReasonRecheckFailed)
	}
	if !guard.Complete || !guard.Resumed {
		run.result.count(ReasonIncompleteAfterRecheck, 1)
		if err := run.deps.Store.AddProposalWarning(ctx, proposalID, warningIncompleteAfterRecheck); err != nil {
			return err
		}
		return run.finish(ctx, proposalID, store.StepSeededPausedIncomplete, linkRoot)
	}
	return run.seeded(ctx, proposalID, infohash, linkRoot)
}

func (run seedRun) seeded(ctx context.Context, proposalID int64, infohash, linkRoot string) error {
	if err := run.finish(ctx, proposalID, store.StepSeeded, linkRoot); err != nil {
		return err
	}
	return run.notify(ctx, proposalID, infohash)
}

func (run seedRun) finish(ctx context.Context, proposalID int64, step store.ProposalStep, linkRoot string) error {
	if err := run.deps.Store.ApplyProposal(ctx, proposalID, step, linkRoot, run.deps.now()); err != nil {
		return err
	}
	run.result.Processed++
	if step == store.StepSeeded {
		run.result.Seeded++
	} else {
		run.result.SeededPausedIncomplete++
	}
	return nil
}

// notify prévient cross-seed en mode Delegate, au mieux : un échec est un avertissement, jamais un échec du seed. En
// Native, AcceptGroup a déjà accepté les autres torrents ; en Disabled, rien.
func (run seedRun) notify(ctx context.Context, proposalID int64, infohash string) error {
	if run.deps.CrossSeed != crossseed.Delegate || run.deps.Notifier == nil {
		return nil
	}
	if err := run.deps.Notifier.Notify(ctx, infohash); err != nil {
		run.result.count(ReasonCrossSeedNotifyFailed, 1)
		return run.deps.Store.AddProposalWarning(context.WithoutCancel(ctx), proposalID, warningCrossSeedNotifyFailed)
	}
	return nil
}
