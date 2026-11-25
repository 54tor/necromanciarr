package pipeline

import (
	"context"
	"errors"
	"os"
	"strings"

	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/matcher"
	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
	"necromanciarr/internal/torznab"
)

// Raisons comptées par l'étape Verify (des noms, jamais un chemin ni un secret).
const (
	ReasonTorrentNotFound    = "torrent_not_found"
	ReasonTorrentFetchFailed = "torrent_fetch_failed"
	ReasonTorrentTooLarge    = "torrent_too_large"
	ReasonInvalidTorrent     = "invalid_torrent"
	ReasonV2Only             = "v2_only"
	// ReasonIndexerBudgetExhausted : un plafond de requêtes à un indexeur est atteint ; pas un échec, la suite attend
	// la prochaine exécution.
	ReasonIndexerBudgetExhausted = "indexer_budget_exhausted"
	// Raisons du secours par les indexeurs : un indexeur laissé pour cette exécution (le candidat reste matched et sera
	// repris), ou un élément refusé (la recherche continue) ; aucune ne cite de lien, de clé ni de nom de release.
	ReasonIndexerUnavailable     = "indexer_unavailable"
	ReasonIndexerAccessDenied    = "indexer_access_denied"
	ReasonIndexerHostileResponse = "indexer_hostile_response"
	ReasonForeignDownloadHost    = "foreign_download_host"
	ReasonMagnetOnly             = "magnet_only"
	ReasonInfohashMismatch       = "infohash_mismatch"
	ReasonNoFileMatched          = "no_file_matched"
	ReasonChangedFile            = "changed_file_stale"
	// ReasonReadError compte une lecture locale en échec : le candidat garde son avancement et sera repris.
	ReasonReadError        = "read_error"
	ReasonReadBudget       = "read_budget_exceeded"
	ReasonNfoFetchFailed   = "nfo_fetch_failed"
	ReasonMediaNotVerified = "media_not_verified"
	// ReasonMediaHashFailed : un média présent a une pièce au SHA1 faux (mauvais fichier ou corruption) ; candidat failed.
	ReasonMediaHashFailed = "media_hash_failed"
	// ReasonMissingPiecesAboveCap : un partial_annex ou partial_pack manque plus de pièces que partialMaxMissingPercent ;
	// son verdict est mis de côté, aucune proposition n'est créée.
	ReasonMissingPiecesAboveCap = "missing_pieces_above_cap"
)

// runVerify efface d'abord les blocs scellés plus vieux que store.SealedRetention, puis vérifie, un candidat à la fois,
// chaque candidat matched ou fetched (reprise), applique le plafond de pièces manquantes courant (reevaluateCap), puis
// met à jour les propositions de groupe ; une annulation enregistre l'avancement cohérent et arrête l'étape, une perte
// du verrou arrête toute écriture.
func runVerify(ctx context.Context, deps Deps) (Result, error) {
	var result Result
	if _, err := deps.Store.PurgeSealedOlderThan(ctx, deps.now(), store.SealedRetention); err != nil {
		return result, err
	}
	candidates, err := deps.Store.ListCandidatesByState(ctx, store.CandidateMatched, store.CandidateFetched)
	if err != nil {
		return result, err
	}
	mediaFiles, err := deps.Store.ListMediaFiles(ctx)
	if err != nil {
		return result, err
	}
	run := candidateRun{deps: deps, result: &result, mediaByID: map[int64]store.MediaFile{}, budget: deps.Sources.NewBudget()}
	for _, mediaFile := range mediaFiles {
		run.mediaByID[mediaFile.ID] = mediaFile
	}
	var stageErr error
	for _, candidate := range candidates {
		if stageErr = ctx.Err(); stageErr != nil {
			break
		}
		if stageErr = run.verify(ctx, candidate); stageErr != nil {
			break
		}
	}
	if lockLost(ctx) {
		return result, stageErr
	}
	finalCtx := context.WithoutCancel(ctx)
	return result, errors.Join(stageErr, reevaluateCap(finalCtx, deps, &result), refreshGroups(finalCtx, deps, &result))
}

// candidateRun porte ce qui sert à vérifier un candidat ; une erreur renvoyée arrête l'étape, un échec propre au
// candidat est compté et la boucle continue.
type candidateRun struct {
	deps      Deps
	result    *Result
	mediaByID map[int64]store.MediaFile
	// budget borne les requêtes aux indexeurs pendant cette exécution ; le fournisseur ne le consulte pas.
	budget *sources.Budget
}

func (run candidateRun) verify(ctx context.Context, candidate store.Candidate) error {
	associations, err := run.deps.Store.ListCandidateFiles(ctx, candidate.ID)
	if err != nil {
		return err
	}
	local, unchanged := run.currentLocalFiles(associations)
	if !unchanged {
		return run.markStale(ctx, candidate)
	}
	torrent, failure, err := run.loadTorrent(ctx, candidate)
	if errors.Is(err, errCandidateDeferred) {
		return nil
	}
	if err != nil || failure != "" {
		return run.failOrStop(ctx, candidate, failure, err)
	}
	opener := newFileOpener(run.deps.openFile())
	defer opener.closeAll()
	assignment, proceed, err := run.assign(ctx, candidate, torrent, local, opener)
	if err != nil || !proceed {
		return err
	}
	annexes, err := run.storedAnnexes(ctx, candidate, torrent)
	if err != nil {
		return err
	}
	source, err := buildSource(assignment, annexes, opener)
	if err != nil {
		run.countReadError()
		return nil
	}
	source, err = run.completeNfos(ctx, candidate, torrent, assignment, source)
	if err != nil {
		return run.readErrorOrStop(ctx, err)
	}
	report, complete, err := run.cover(ctx, candidate, torrent, source)
	if err != nil || !complete {
		return err
	}
	return run.finish(ctx, candidate, associations, torrent, assignment, source, report)
}

// currentLocalFiles renvoie les fichiers locaux associés, tels que sur disque ; unchanged est faux si l'un a disparu
// ou a changé de taille ou de date depuis l'association : un bitset enregistré ne vaudrait alors plus rien.
func (run candidateRun) currentLocalFiles(associations []store.CandidateFile) ([]store.MediaFile, bool) {
	seen := map[int64]bool{}
	var local []store.MediaFile
	for _, association := range associations {
		if association.MediaFileID == 0 || seen[association.MediaFileID] {
			continue
		}
		seen[association.MediaFileID] = true
		mediaFile, known := run.mediaByID[association.MediaFileID]
		if !known {
			return nil, false
		}
		info, err := os.Stat(mediaFile.Path)
		if err != nil || info.Size() != association.MediaSize || !info.ModTime().Equal(association.MediaModTime) {
			return nil, false
		}
		// Taille et date de l'instantané d'association, identiques au disque : ce sont elles que Link recontrôlera.
		mediaFile.Size, mediaFile.ModTime = association.MediaSize, association.MediaModTime
		local = append(local, mediaFile)
	}
	return local, true
}

func (run candidateRun) markStale(ctx context.Context, candidate store.Candidate) error {
	run.result.Skipped++
	run.result.count(ReasonChangedFile, 1)
	return run.deps.Store.SetCandidateState(ctx, candidate.ID, store.CandidateStale, run.deps.now())
}

// failOrStop passe le candidat à failed pour une raison nommée, ou renvoie l'erreur qui arrête l'étape.
func (run candidateRun) failOrStop(ctx context.Context, candidate store.Candidate, failure string, err error) error {
	if err != nil {
		return err
	}
	run.result.Failed++
	run.result.count(failure, 1)
	return run.deps.Store.SetCandidateState(ctx, candidate.ID, store.CandidateFailed, run.deps.now())
}

// loadTorrent reprend le .torrent conservé d'un candidat fetched, sinon le télécharge, le contrôle et le conserve.
func (run candidateRun) loadTorrent(ctx context.Context, candidate store.Candidate) (*torrentcheck.Torrent, string, error) {
	if candidate.State == store.CandidateFetched {
		raw, err := run.deps.Store.CandidateTorrent(ctx, candidate.ID)
		if err != nil {
			return nil, "", err
		}
		if len(raw) > 0 {
			torrent, failure := parseCandidateTorrent(raw, candidate)
			return torrent, failure, nil
		}
	}
	release := sources.Release{
		Infohash: candidate.Infohash, Tracker: candidate.Tracker, Name: candidate.Name,
		AnnounceConfigured: run.deps.Announces.Resolve(candidate.Tracker, false).Source == sources.AnnounceConfig,
	}
	fetched, err := run.deps.Sources.FetchTorrent(ctx, release, run.budget)
	if err != nil {
		if deferred := run.recordFallback(candidate, err); deferred {
			return nil, "", errCandidateDeferred
		}
		failure, stopErr := fetchFailure(err)
		return nil, failure, stopErr
	}
	if len(fetched.Sanitized) > store.MaxTorrentBytes {
		return nil, ReasonTorrentTooLarge, nil
	}
	torrent, failure := parseCandidateTorrent(fetched.Sanitized, candidate)
	if failure != "" {
		return nil, failure, nil
	}
	// La chaîne livre déjà une copie assainie ; l'assainissement est idempotent et garde la colonne sans annonce même
	// si une source l'oubliait.
	sanitized, failure := sanitizedTorrent(fetched.Sanitized)
	if failure != "" {
		return nil, failure, nil
	}
	if err := run.saveFetched(ctx, candidate, fetched, sanitized); err != nil {
		return nil, "", err
	}
	return torrent, "", run.deps.Store.SetCandidateState(ctx, candidate.ID, store.CandidateFetched, run.deps.now())
}

// fetchFailure classe une erreur du fournisseur : arrêt de l'étape (annulation, accès refusé, fournisseur limité ou
// indisponible, à reprendre plus tard) ou échec de ce seul candidat.
func fetchFailure(err error) (string, error) {
	if stopErr := stopError(StageVerify, err); stopErr != nil {
		return "", stopErr
	}
	// Le v2 seul avant tout le reste : torznab.ErrBadResponse l'englobe.
	if errors.Is(err, torrentcheck.ErrV2Only) {
		return ReasonV2Only, nil
	}
	if errors.Is(err, sources.ErrInvalidTorrent) || errors.Is(err, torrentcheck.ErrInvalidTorrent) {
		return ReasonInvalidTorrent, nil
	}
	if errors.Is(err, torznab.ErrInfohashMismatch) {
		return ReasonInfohashMismatch, nil
	}
	if errors.Is(err, libraryprovider.ErrNotFound) {
		return ReasonTorrentNotFound, nil
	}
	if errors.Is(err, libraryprovider.ErrResponseTooLarge) || errors.Is(err, sources.ErrTorrentTooLarge) {
		return ReasonTorrentTooLarge, nil
	}
	if errors.Is(err, sources.ErrSecretKeyMissing) {
		return ReasonSecretKeyMissing, nil
	}
	return ReasonTorrentFetchFailed, nil
}

// sanitizedTorrent rend la seule copie conservée, sans annonce ; info est recopié à l'identique, l'infohash ne change
// pas. Un .torrent que l'assainissement refuse est un échec du candidat, jamais de l'étape.
func sanitizedTorrent(raw []byte) ([]byte, string) {
	sanitized, err := sources.Sanitize(raw)
	if err != nil {
		return nil, ReasonInvalidTorrent
	}
	return sanitized, ""
}

// parseCandidateTorrent refuse un .torrent v2 seul, invalide, ou dont l'infohash n'est pas celui du candidat.
func parseCandidateTorrent(raw []byte, candidate store.Candidate) (*torrentcheck.Torrent, string) {
	torrent, err := torrentcheck.Parse(raw)
	if errors.Is(err, torrentcheck.ErrV2Only) {
		return nil, ReasonV2Only
	}
	if err != nil {
		return nil, ReasonInvalidTorrent
	}
	if !strings.EqualFold(torrent.InfoHash, candidate.Infohash) {
		return nil, ReasonInfohashMismatch
	}
	return torrent, ""
}

// assign associe les fichiers locaux aux fichiers du torrent par taille puis par hash ; proceed est faux quand le
// candidat a été traité (aucun fichier ne correspond, ou lecture en échec à reprendre).
func (run candidateRun) assign(ctx context.Context, candidate store.Candidate, torrent *torrentcheck.Torrent,
	local []store.MediaFile, opener *fileOpener,
) (matcher.Assignment, bool, error) {
	options := matcher.Options{Budget: run.deps.ReadBudget, Limiter: run.deps.ReadLimiter}
	assignment, err := options.Assign(ctx, torrent, local, opener.readerAt)
	switch {
	case err == nil:
	case errors.Is(err, matcher.ErrReadBudgetExceeded):
		// Le reste est rangé dans Ambiguous : il deviendra un litige, jamais un choix.
		run.result.count(ReasonReadBudget, 1)
	case ctx.Err() != nil:
		return assignment, false, err
	default:
		run.countReadError()
		return assignment, false, nil
	}
	if len(assignment.ByTorrentFile) == 0 && len(assignment.Ambiguous) == 0 {
		return assignment, false, run.failOrStop(ctx, candidate, ReasonNoFileMatched, nil)
	}
	return assignment, true, nil
}

// readErrorOrStop arrête l'étape sur annulation ou arrêt du fournisseur ; une lecture locale en échec n'arrête que ce
// candidat, qui reste fetched et sera repris.
func (run candidateRun) readErrorOrStop(ctx context.Context, err error) error {
	var resumable *ResumableError
	isStop := ctx.Err() != nil || errors.As(err, &resumable) || errors.Is(err, libraryprovider.ErrAccessDenied)
	if isStop {
		return err
	}
	if errors.Is(err, errReadLocal) {
		run.countReadError()
		return nil
	}
	return err
}

func (run candidateRun) countReadError() {
	run.result.Failed++
	run.result.count(ReasonReadError, 1)
}

// finish relit l'état des fichiers (un fichier modifié pendant la lecture périme le candidat), puis : un média au hash
// faux rend le candidat failed (vérification gardée pour la revue, aucune proposition) ; sinon la vérification,
// l'association prouvée (sans les annexes exclues) et l'état verified sont écrits ensemble, et la proposition créée.
func (run candidateRun) finish(ctx context.Context, candidate store.Candidate, associations []store.CandidateFile,
	torrent *torrentcheck.Torrent, assignment matcher.Assignment, source compositeSource, report torrentcheck.Report,
) error {
	if _, unchanged := run.currentLocalFiles(associations); !unchanged {
		return run.markStale(ctx, candidate)
	}
	decided := classify(torrent, assignment, source, report)
	verification := completedVerification(candidate.ID, report)
	if decided.ineligible != "" {
		if err := run.deps.Store.SaveVerification(ctx, verification, run.deps.now()); err != nil {
			return err
		}
		return run.failOrStop(ctx, candidate, decided.ineligible, nil)
	}
	files := assignedFiles(assignment, decided.excluded)
	if err := run.deps.Store.CompleteVerification(ctx, verification, files, run.deps.now()); err != nil {
		return err
	}
	run.result.Verified++
	run.result.Processed++
	return run.propose(ctx, candidate, decided)
}

// assignedFiles traduit l'association prouvée par le hash, sans les fichiers exclus.
func assignedFiles(assignment matcher.Assignment, excluded map[int]bool) []store.AssignedFile {
	var files []store.AssignedFile
	for torrentFileIndex, mediaFile := range assignment.ByTorrentFile {
		if excluded[torrentFileIndex] {
			continue
		}
		files = append(files, store.AssignedFile{
			TorrentFileIndex: torrentFileIndex, MediaFileID: mediaFile.ID, Path: mediaFile.Path, Size: mediaFile.Size, ModTime: mediaFile.ModTime,
		})
	}
	return files
}
