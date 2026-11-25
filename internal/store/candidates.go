package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// CandidateState est l'état d'un torrent candidat dans le pipeline.
type CandidateState string

// États d'un candidat : matched (tailles concordantes), fetched (.torrent téléchargé), verified (pièces contrôlées),
// failed (échec d'une étape), stale (un fichier associé a disparu ou changé).
const (
	CandidateMatched  CandidateState = "matched"
	CandidateFetched  CandidateState = "fetched"
	CandidateVerified CandidateState = "verified"
	CandidateFailed   CandidateState = "failed"
	CandidateStale    CandidateState = "stale"
)

// SaveOutcome dit ce que SaveCandidate a fait.
type SaveOutcome string

// Issues de SaveCandidate : créé, mis à jour (fichiers changés ou candidat périmé, retour à matched), inchangé.
const (
	SaveCreated   SaveOutcome = "created"
	SaveUpdated   SaveOutcome = "updated"
	SaveUnchanged SaveOutcome = "unchanged"
)

// MaxTorrentBytes plafonne le .torrent conservé : au-delà, l'étape qui le télécharge échoue proprement.
const MaxTorrentBytes = 8 << 20

// Erreurs typées liées à un candidat.
var (
	ErrUnknownCandidateState = errors.New("store: unknown candidate state")
	ErrCandidateNotFound     = errors.New("store: candidate not found")
	ErrTorrentTooLarge       = errors.New("store: torrent file exceeds 8 MiB")
	// ErrInvalidTransition signale un changement d'état de candidat ou de décision de proposition interdit.
	ErrInvalidTransition = errors.New("store: invalid state transition")
)

// Candidate est une release candidate, identifiée par (Infohash, Tracker) ; GroupID regroupe chaque candidat qui se
// dispute un même fichier de la bibliothèque (0 tant que RegroupCandidates n'est pas passé). Generation augmente à
// chaque invalidation (stale, réassociation, relance) : une vérification et ses propositions valent pour une génération.
// Source est la source qui l'a fourni (SourceProvider ou « torznab:<nom> ») ; le bloc scellé n'est jamais exposé ici
// (CandidateSealed).
type Candidate struct {
	ID           int64
	Infohash     string
	Tracker      string
	Name         string
	TotalSize    int64
	ProviderJSON string
	Source       string
	// HasSealed dit si le candidat a un .torrent d'indexeur scellé (lu, jamais écrit par SaveCandidate) ; le bloc
	// lui-même ne passe que par CandidateSealed.
	HasSealed  bool
	State      CandidateState
	GroupID    int64
	Generation int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// CandidateFile associe un fichier du torrent (FileIndex) à un fichier de la bibliothèque ; MediaFileID vaut 0 si aucun
// fichier local n'a sa taille. MediaSize et MediaModTime figent l'état du fichier local au moment de l'association,
// pour détecter qu'il a changé. Ambiguous signale plusieurs fichiers locaux possibles (une ligne par fichier).
type CandidateFile struct {
	CandidateID  int64
	FileIndex    int
	Path         string
	Size         int64
	MediaFileID  int64
	MediaSize    int64
	MediaModTime time.Time
	Ambiguous    bool
}

func checkCandidateState(state CandidateState) error {
	switch state {
	case CandidateMatched, CandidateFetched, CandidateVerified, CandidateFailed, CandidateStale:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnknownCandidateState, state)
	}
}

// SetMediaFileMetadata remplace identifiants et métadonnées du fichier (0 et "" effacent).
func (openedStore *Store) SetMediaFileMetadata(ctx context.Context, mediaFileID int64, metadata MediaMetadata) error {
	_, err := openedStore.database.ExecContext(ctx, `UPDATE media_files
SET tmdb_id = ?, tvdb_id = ?, imdb_id = ?, title = ?, year = ?, season = ?, episode = ? WHERE id = ?`,
		metadata.TMDBID, metadata.TVDBID, metadata.IMDBID, metadata.Title, metadata.Year, metadata.Season, metadata.Episode, mediaFileID)
	if err != nil {
		return fmt.Errorf("set media file metadata: %w", err)
	}
	return nil
}

// DeleteMediaFile retire un fichier disparu de l'inventaire ; chaque candidat associé devient stale avant la suppression.
func (openedStore *Store) DeleteMediaFile(ctx context.Context, mediaFileID int64, now time.Time) error {
	return openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		if _, err := invalidateForFile(ctx, transaction, mediaFileID, now); err != nil {
			return err
		}
		_, err := transaction.ExecContext(ctx, `DELETE FROM media_files WHERE id = ?`, mediaFileID)
		return err
	})
}

// InvalidateCandidatesForFile passe à stale chaque candidat associé au fichier (voir markStale) ; renvoie le nombre
// de lignes touchées.
func (openedStore *Store) InvalidateCandidatesForFile(ctx context.Context, mediaFileID int64, now time.Time) (int, error) {
	var invalidated int
	err := openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		var err error
		invalidated, err = invalidateForFile(ctx, transaction, mediaFileID, now)
		return err
	})
	return invalidated, err
}

const candidatesOfFile = `SELECT candidate_id FROM candidate_files WHERE media_file_id = ?`

func invalidateForFile(ctx context.Context, transaction *sql.Tx, mediaFileID int64, now time.Time) (int, error) {
	invalidated, err := markStale(ctx, transaction, candidatesOfFile, now, mediaFileID)
	if err != nil {
		return 0, fmt.Errorf("invalidate candidates: %w", err)
	}
	return invalidated, nil
}

// changedCandidates sélectionne chaque candidat dont un fichier local associé a disparu ou a changé de taille ou de date.
const changedCandidates = `SELECT candidate_files.candidate_id FROM candidate_files
LEFT JOIN media_files ON media_files.id = candidate_files.media_file_id
WHERE candidate_files.media_size IS NOT NULL AND (media_files.id IS NULL OR media_files.size != candidate_files.media_size
	OR media_files.mtime != candidate_files.media_mtime)`

// MarkChangedCandidatesStale passe à stale (voir markStale) tout candidat dont un fichier local associé a disparu ou
// changé depuis l'association ; renvoie le nombre de lignes touchées.
func (openedStore *Store) MarkChangedCandidatesStale(ctx context.Context, now time.Time) (int, error) {
	var marked int
	err := openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		var err error
		marked, err = markStale(ctx, transaction, changedCandidates, now)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("mark changed candidates stale: %w", err)
	}
	return marked, nil
}

// markStale périme chaque candidat choisi par selection dans la transaction : vérification et association prouvée
// effacées, propositions pending, accepted et failed closes (superseded), groupe retiré, génération suivante ; les
// propositions appliquées restent comme historique.
func markStale(ctx context.Context, transaction *sql.Tx, selection string, now time.Time, arguments ...any) (int, error) {
	if err := clearVerification(ctx, transaction, selection, arguments...); err != nil {
		return 0, err
	}
	if err := supersedeProposals(ctx, transaction, nonTerminalDecisions, selection, now, arguments...); err != nil {
		return 0, err
	}
	updateArguments := append([]any{string(CandidateStale), now.UnixNano(), string(CandidateStale)}, arguments...)
	// #nosec G202 -- sélection constante du paquet
	result, err := transaction.ExecContext(ctx, `UPDATE torrent_candidates SET state = ?, updated_at = ?, group_id = NULL,
	generation = generation + 1 WHERE state != ? AND id IN (`+selection+`)`, updateArguments...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	return int(affected), err
}

// clearVerification efface la vérification et l'association prouvée de chaque candidat choisi par selection.
func clearVerification(ctx context.Context, transaction *sql.Tx, selection string, arguments ...any) error {
	for _, table := range []string{"verifications", "verified_assignments"} {
		// #nosec G202 -- table et sélection constantes du paquet
		if _, err := transaction.ExecContext(ctx, `DELETE FROM `+table+` WHERE candidate_id IN (`+selection+`)`, arguments...); err != nil {
			return err
		}
	}
	return nil
}

// nonTerminalDecisions sont les décisions closes (superseded) quand un candidat est périmé, réassocié, relancé ou
// échoue : elles pourraient encore mener à applied. applied, rejected et superseded restent tels quels (rejected ne
// passe à superseded que par SetProposalDecision).
var nonTerminalDecisions = []ProposalDecision{ProposalPending, ProposalAccepted, ProposalFailed}

func supersedeProposals(ctx context.Context, transaction *sql.Tx, decisions []ProposalDecision, selection string, now time.Time, arguments ...any) error {
	proposalArguments := []any{string(ProposalSuperseded), now.UnixNano()}
	for _, decision := range decisions {
		proposalArguments = append(proposalArguments, string(decision))
	}
	proposalArguments = append(proposalArguments, arguments...)
	placeholders := "?" + strings.Repeat(", ?", len(decisions)-1)
	// #nosec G202 -- sélection constante du paquet, marqueurs générés
	_, err := transaction.ExecContext(ctx, `UPDATE proposals SET decision = ?, decided_at = ?
WHERE decision IN (`+placeholders+`) AND candidate_id IN (`+selection+`)`, proposalArguments...)
	return err
}

// SaveCandidate crée le candidat (état matched) ou met à jour celui de même (Infohash, Tracker) ; s'il est périmé ou
// si ses associations de fichiers ont changé, elles sont remplacées, sa vérification effacée et il revient à matched ;
// sinon son état est gardé. Source vide vaut SourceProvider ; la source d'un candidat déjà en base reste la première
// enregistrée. Rejouable : jamais de doublon.
func (openedStore *Store) SaveCandidate(ctx context.Context, candidate Candidate, files []CandidateFile, now time.Time) (int64, SaveOutcome, error) {
	var candidateID int64
	var outcome SaveOutcome
	err := openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		var err error
		candidateID, outcome, err = saveCandidate(ctx, transaction, candidate, files, now)
		return err
	})
	if err != nil {
		return 0, "", fmt.Errorf("save candidate: %w", err)
	}
	return candidateID, outcome, nil
}

func saveCandidate(ctx context.Context, transaction *sql.Tx, candidate Candidate, files []CandidateFile, now time.Time) (int64, SaveOutcome, error) {
	providerJSON := candidate.ProviderJSON
	if providerJSON == "" {
		providerJSON = "{}"
	}
	var candidateID int64
	var state string
	err := transaction.QueryRowContext(ctx, `SELECT id, state FROM torrent_candidates WHERE infohash = ? AND tracker = ?`,
		candidate.Infohash, candidate.Tracker).Scan(&candidateID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return insertCandidate(ctx, transaction, candidate, providerJSON, files, now)
	}
	if err != nil {
		return 0, "", err
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE torrent_candidates SET name = ?, total_size = ?, provider_json = ? WHERE id = ?`,
		candidate.Name, candidate.TotalSize, providerJSON, candidateID); err != nil {
		return 0, "", err
	}
	storedFiles, err := listCandidateFiles(ctx, transaction, candidateID)
	if err != nil {
		return 0, "", err
	}
	filesUnchanged := sameCandidateFiles(storedFiles, files)
	if filesUnchanged && CandidateState(state) != CandidateStale {
		return candidateID, SaveUnchanged, nil
	}
	return candidateID, SaveUpdated, resetCandidate(ctx, transaction, candidateID, files, now)
}

func insertCandidate(ctx context.Context, transaction *sql.Tx, candidate Candidate, providerJSON string, files []CandidateFile, now time.Time) (int64, SaveOutcome, error) {
	source := candidate.Source
	if source == "" {
		source = SourceProvider
	}
	result, err := transaction.ExecContext(ctx, `INSERT INTO torrent_candidates
	(infohash, tracker, name, total_size, provider_json, source, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		candidate.Infohash, candidate.Tracker, candidate.Name, candidate.TotalSize, providerJSON, source, string(CandidateMatched),
		now.UnixNano(), now.UnixNano())
	if err != nil {
		return 0, "", err
	}
	candidateID, err := result.LastInsertId()
	if err != nil {
		return 0, "", err
	}
	return candidateID, SaveCreated, insertCandidateFiles(ctx, transaction, candidateID, files)
}

// resetCandidate remplace les associations et ramène le candidat à matched ; ses propositions ouvertes sont closes,
// jamais ressuscitées.
func resetCandidate(ctx context.Context, transaction *sql.Tx, candidateID int64, files []CandidateFile, now time.Time) error {
	if err := supersedeProposals(ctx, transaction, nonTerminalDecisions, `SELECT ?`, now, candidateID); err != nil {
		return err
	}
	if err := clearVerification(ctx, transaction, `SELECT ?`, candidateID); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM candidate_files WHERE candidate_id = ?`, candidateID); err != nil {
		return err
	}
	if err := restartCandidate(ctx, transaction, candidateID, now); err != nil {
		return err
	}
	return insertCandidateFiles(ctx, transaction, candidateID, files)
}

func insertCandidateFiles(ctx context.Context, transaction *sql.Tx, candidateID int64, files []CandidateFile) error {
	for _, file := range files {
		var mediaFileID, mediaSize, mediaModTime any
		if file.MediaFileID != 0 {
			mediaFileID, mediaSize, mediaModTime = file.MediaFileID, file.MediaSize, file.MediaModTime.UnixNano()
		}
		_, err := transaction.ExecContext(ctx, `INSERT INTO candidate_files
	(candidate_id, file_index, path, size, media_file_id, media_size, media_mtime, ambiguous) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			candidateID, file.FileIndex, file.Path, file.Size, mediaFileID, mediaSize, mediaModTime, file.Ambiguous)
		if err != nil {
			return fmt.Errorf("insert candidate file: %w", err)
		}
	}
	return nil
}

func sameCandidateFiles(stored, wanted []CandidateFile) bool {
	normalized := func(files []CandidateFile) []CandidateFile {
		copied := make([]CandidateFile, len(files))
		for fileIndex, file := range files {
			file.CandidateID = 0
			file.MediaModTime = file.MediaModTime.UTC()
			copied[fileIndex] = file
		}
		slices.SortFunc(copied, compareCandidateFiles)
		return copied
	}
	return slices.EqualFunc(normalized(stored), normalized(wanted), func(left, right CandidateFile) bool {
		return left.FileIndex == right.FileIndex && left.Path == right.Path && left.Size == right.Size &&
			left.MediaFileID == right.MediaFileID && left.MediaSize == right.MediaSize &&
			left.MediaModTime.Equal(right.MediaModTime) && left.Ambiguous == right.Ambiguous
	})
}

func compareCandidateFiles(left, right CandidateFile) int {
	if left.FileIndex != right.FileIndex {
		return left.FileIndex - right.FileIndex
	}
	return int(left.MediaFileID - right.MediaFileID)
}

// queryer est commun à *sql.DB et *sql.Tx.
type queryer interface {
	QueryContext(ctx context.Context, query string, arguments ...any) (*sql.Rows, error)
}

// ListCandidateFiles renvoie les associations du candidat, triées par index de fichier puis par fichier local.
func (openedStore *Store) ListCandidateFiles(ctx context.Context, candidateID int64) ([]CandidateFile, error) {
	files, err := listCandidateFiles(ctx, openedStore.database, candidateID)
	if err != nil {
		return nil, fmt.Errorf("list candidate files: %w", err)
	}
	return files, nil
}

func listCandidateFiles(ctx context.Context, database queryer, candidateID int64) ([]CandidateFile, error) {
	rows, err := database.QueryContext(ctx, `SELECT file_index, path, size, media_file_id, media_size, media_mtime, ambiguous
FROM candidate_files WHERE candidate_id = ? ORDER BY file_index, media_file_id`, candidateID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var files []CandidateFile
	for rows.Next() {
		file := CandidateFile{CandidateID: candidateID}
		var mediaFileID, mediaSize, mediaModTime sql.NullInt64
		if err := rows.Scan(&file.FileIndex, &file.Path, &file.Size, &mediaFileID, &mediaSize, &mediaModTime, &file.Ambiguous); err != nil {
			return nil, err
		}
		file.MediaFileID, file.MediaSize = mediaFileID.Int64, mediaSize.Int64
		if mediaModTime.Valid {
			file.MediaModTime = time.Unix(0, mediaModTime.Int64).UTC()
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

// ListCandidatesByState renvoie chaque candidat dans l'un des états donnés (tous si aucun), triés par identifiant.
func (openedStore *Store) ListCandidatesByState(ctx context.Context, states ...CandidateState) ([]Candidate, error) {
	query := `SELECT id, infohash, tracker, name, total_size, provider_json, source, torrent_sealed IS NOT NULL, state, group_id,
	generation, created_at, updated_at FROM torrent_candidates`
	arguments := make([]any, 0, len(states))
	if len(states) > 0 {
		query += ` WHERE state IN (?` + strings.Repeat(", ?", len(states)-1) + `)`
		for _, state := range states {
			arguments = append(arguments, string(state))
		}
	}
	rows, err := openedStore.database.QueryContext(ctx, query+` ORDER BY id`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var candidates []Candidate
	for rows.Next() {
		var candidate Candidate
		var state string
		var groupID sql.NullInt64
		var createdAt, updatedAt int64
		if err := rows.Scan(&candidate.ID, &candidate.Infohash, &candidate.Tracker, &candidate.Name, &candidate.TotalSize,
			&candidate.ProviderJSON, &candidate.Source, &candidate.HasSealed, &state, &groupID, &candidate.Generation, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan candidate: %w", err)
		}
		candidate.State, candidate.GroupID = CandidateState(state), groupID.Int64
		candidate.CreatedAt, candidate.UpdatedAt = time.Unix(0, createdAt).UTC(), time.Unix(0, updatedAt).UTC()
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// SetCandidateState change l'état d'un candidat selon les transitions autorisées : matched → fetched → verified, et
// tout état → stale ou failed ; fetched → fetched et verified → verified sont sans effet (une étape rejouée n'échoue
// pas). Le retour à matched ne passe que par SaveCandidate (candidat périmé, ou failed dont les fichiers ont changé)
// et par RetryCandidate (failed). Sinon ErrInvalidTransition. Passer à stale applique markStale ; passer à failed clôt
// les propositions ouvertes ; dans la même transaction.
func (openedStore *Store) SetCandidateState(ctx context.Context, candidateID int64, state CandidateState, now time.Time) error {
	if err := checkCandidateState(state); err != nil {
		return err
	}
	return openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		current, err := candidateStateOf(ctx, transaction, candidateID)
		if err != nil {
			return err
		}
		if isIdempotentRepeat(current, state) {
			return nil
		}
		if !candidateTransitionAllowed(current, state) {
			return fmt.Errorf("%w: candidate %s -> %s", ErrInvalidTransition, current, state)
		}
		switch state {
		case CandidateStale:
			_, err = markStale(ctx, transaction, `SELECT ?`, now, candidateID)
			return err
		case CandidateFailed:
			if err := supersedeProposals(ctx, transaction, nonTerminalDecisions, `SELECT ?`, now, candidateID); err != nil {
				return err
			}
			// Un candidat failed n'a plus d'association prouvée ; sa vérification reste pour la revue.
			if _, err := transaction.ExecContext(ctx, `DELETE FROM verified_assignments WHERE candidate_id = ?`, candidateID); err != nil {
				return err
			}
		}
		return writeCandidateState(ctx, transaction, candidateID, state, now)
	})
}

// RetryCandidate ramène un candidat failed à matched pour une nouvelle tentative : vérification effacée, propositions
// ouvertes closes, dans la même transaction ; ErrInvalidTransition depuis tout autre état.
func (openedStore *Store) RetryCandidate(ctx context.Context, candidateID int64, now time.Time) error {
	return openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		current, err := candidateStateOf(ctx, transaction, candidateID)
		if err != nil {
			return err
		}
		if current != CandidateFailed {
			return fmt.Errorf("%w: retry candidate from %s", ErrInvalidTransition, current)
		}
		if err := clearVerification(ctx, transaction, `SELECT ?`, candidateID); err != nil {
			return err
		}
		if err := supersedeProposals(ctx, transaction, nonTerminalDecisions, `SELECT ?`, now, candidateID); err != nil {
			return err
		}
		return restartCandidate(ctx, transaction, candidateID, now)
	})
}

// restartCandidate ramène le candidat à matched dans une nouvelle génération.
func restartCandidate(ctx context.Context, transaction *sql.Tx, candidateID int64, now time.Time) error {
	_, err := transaction.ExecContext(ctx, `UPDATE torrent_candidates SET state = ?, updated_at = ?, generation = generation + 1
WHERE id = ?`, string(CandidateMatched), now.UnixNano(), candidateID)
	return err
}

func candidateStateOf(ctx context.Context, transaction *sql.Tx, candidateID int64) (CandidateState, error) {
	var current string
	err := transaction.QueryRowContext(ctx, `SELECT state FROM torrent_candidates WHERE id = ?`, candidateID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrCandidateNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load candidate state: %w", err)
	}
	return CandidateState(current), nil
}

func writeCandidateState(ctx context.Context, transaction *sql.Tx, candidateID int64, state CandidateState, now time.Time) error {
	_, err := transaction.ExecContext(ctx, `UPDATE torrent_candidates SET state = ?, updated_at = ? WHERE id = ?`,
		string(state), now.UnixNano(), candidateID)
	if err != nil {
		return fmt.Errorf("set candidate state: %w", err)
	}
	return nil
}

func isIdempotentRepeat(from, to CandidateState) bool {
	return from == to && (to == CandidateFetched || to == CandidateVerified)
}

func candidateTransitionAllowed(from, to CandidateState) bool {
	switch {
	case to == CandidateStale || to == CandidateFailed:
		return true
	case from == CandidateMatched && to == CandidateFetched:
		return true
	case from == CandidateFetched && to == CandidateVerified:
		return true
	}
	return false
}

// SaveCandidateTorrent conserve le .torrent téléchargé, sans annonce : au-delà de MaxTorrentBytes, ErrTorrentTooLarge ;
// avec une clé announce ou announce-list au premier niveau, ou illisible, ErrUnsanitizedTorrent (le .torrent complet
// d'un indexeur va, scellé, dans SaveCandidateSealed).
func (openedStore *Store) SaveCandidateTorrent(ctx context.Context, candidateID int64, raw []byte, now time.Time) error {
	if len(raw) > MaxTorrentBytes {
		return ErrTorrentTooLarge
	}
	if err := refuseUnsanitized(raw); err != nil {
		return err
	}
	result, err := openedStore.database.ExecContext(ctx, `UPDATE torrent_candidates SET torrent = ?, updated_at = ? WHERE id = ?`,
		raw, now.UnixNano(), candidateID)
	return requireOneRow(result, err, ErrCandidateNotFound, "save candidate torrent")
}

// CandidateTorrent renvoie le .torrent conservé, nil s'il n'a pas encore été téléchargé.
func (openedStore *Store) CandidateTorrent(ctx context.Context, candidateID int64) ([]byte, error) {
	var raw []byte
	err := openedStore.database.QueryRowContext(ctx, `SELECT torrent FROM torrent_candidates WHERE id = ?`, candidateID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCandidateNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load candidate torrent: %w", err)
	}
	return raw, nil
}

// RegroupCandidates donne le même GroupID (le plus petit identifiant du groupe) à chaque candidat non périmé qui
// partage un fichier de la bibliothèque, directement ou par transitivité ; un candidat seul a son propre identifiant.
func (openedStore *Store) RegroupCandidates(ctx context.Context) error {
	return openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		groups, err := computeGroups(ctx, transaction)
		if err != nil {
			return err
		}
		for candidateID, groupID := range groups {
			if _, err := transaction.ExecContext(ctx, `UPDATE torrent_candidates SET group_id = ? WHERE id = ?`, groupID, candidateID); err != nil {
				return fmt.Errorf("regroup candidates: %w", err)
			}
		}
		if err := syncProposalGroups(ctx, transaction); err != nil {
			return err
		}
		return electMissingPrimaries(ctx, transaction)
	})
}

// syncProposalGroups rattache chaque proposition ouverte au groupe courant de son candidat ; une proposition qui change
// de groupe perd le rôle de principale, réélu par electMissingPrimaries dans la même transaction.
func syncProposalGroups(ctx context.Context, transaction *sql.Tx) error {
	_, err := transaction.ExecContext(ctx, `UPDATE proposals
SET group_id = (SELECT group_id FROM torrent_candidates WHERE torrent_candidates.id = proposals.candidate_id),
	is_primary = 0, primary_source = ''
WHERE candidate_id IS NOT NULL AND decision IN (?, ?)
	AND group_id IS NOT (SELECT group_id FROM torrent_candidates WHERE torrent_candidates.id = proposals.candidate_id)`,
		string(ProposalPending), string(ProposalAccepted))
	if err != nil {
		return fmt.Errorf("sync proposal groups: %w", err)
	}
	return nil
}

// bestFullOfGroup choisit la proposition full ouverte de la génération courante au plus fort details.percent, à égalité
// le plus petit candidat : la même règle que l'élection du pipeline.
const bestFullOfGroup = `SELECT proposals.id FROM proposals
JOIN torrent_candidates ON torrent_candidates.id = proposals.candidate_id
WHERE proposals.group_id = ? AND proposals.kind = 'full' AND proposals.decision IN ('pending', 'accepted')
	AND proposals.generation = torrent_candidates.generation
ORDER BY json_extract(proposals.details, '$.percent') DESC, proposals.candidate_id, proposals.id LIMIT 1`

// electMissingPrimaries donne une principale auto à chaque groupe qui a une full ouverte mais aucune principale ouverte.
func electMissingPrimaries(ctx context.Context, transaction *sql.Tx) error {
	groupIDs, err := groupsWithoutPrimary(ctx, transaction)
	if err != nil {
		return err
	}
	for _, groupID := range groupIDs {
		_, err := transaction.ExecContext(ctx, `UPDATE proposals SET is_primary = 1, primary_source = ? WHERE id = (`+bestFullOfGroup+`)`,
			string(PrimaryAuto), groupID)
		if err != nil {
			return fmt.Errorf("elect group primary: %w", err)
		}
	}
	return nil
}

func groupsWithoutPrimary(ctx context.Context, transaction *sql.Tx) ([]int64, error) {
	rows, err := transaction.QueryContext(ctx, `SELECT DISTINCT proposals.group_id FROM proposals
JOIN torrent_candidates ON torrent_candidates.id = proposals.candidate_id
WHERE proposals.kind = 'full' AND proposals.decision IN ('pending', 'accepted') AND proposals.group_id IS NOT NULL
	AND proposals.generation = torrent_candidates.generation
	AND NOT EXISTS (SELECT 1 FROM proposals AS other WHERE other.group_id = proposals.group_id AND other.is_primary = 1
		AND other.decision IN ('pending', 'accepted'))`)
	if err != nil {
		return nil, fmt.Errorf("list groups without primary: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var groupIDs []int64
	for rows.Next() {
		var groupID int64
		if err := rows.Scan(&groupID); err != nil {
			return nil, err
		}
		groupIDs = append(groupIDs, groupID)
	}
	return groupIDs, rows.Err()
}

func computeGroups(ctx context.Context, transaction *sql.Tx) (map[int64]int64, error) {
	rows, err := transaction.QueryContext(ctx, `SELECT torrent_candidates.id, candidate_files.media_file_id FROM torrent_candidates
LEFT JOIN candidate_files ON candidate_files.candidate_id = torrent_candidates.id
WHERE torrent_candidates.state != ?`, string(CandidateStale))
	if err != nil {
		return nil, fmt.Errorf("regroup candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	parents := map[int64]int64{}
	firstCandidateOfFile := map[int64]int64{}
	for rows.Next() {
		var candidateID int64
		var mediaFileID sql.NullInt64
		if err := rows.Scan(&candidateID, &mediaFileID); err != nil {
			return nil, err
		}
		if _, known := parents[candidateID]; !known {
			parents[candidateID] = candidateID
		}
		if !mediaFileID.Valid {
			continue
		}
		if otherCandidate, shared := firstCandidateOfFile[mediaFileID.Int64]; shared {
			unite(parents, candidateID, otherCandidate)
		} else {
			firstCandidateOfFile[mediaFileID.Int64] = candidateID
		}
	}
	groups := map[int64]int64{}
	for candidateID := range parents {
		groups[candidateID] = findRoot(parents, candidateID)
	}
	return groups, rows.Err()
}

// unite rattache les deux ensembles sous la plus petite racine, pour que GroupID soit le plus petit identifiant.
func unite(parents map[int64]int64, left, right int64) {
	leftRoot, rightRoot := findRoot(parents, left), findRoot(parents, right)
	parents[max(leftRoot, rightRoot)] = min(leftRoot, rightRoot)
}

func findRoot(parents map[int64]int64, candidateID int64) int64 {
	for parents[candidateID] != candidateID {
		candidateID = parents[candidateID]
	}
	return candidateID
}

func (openedStore *Store) inTransaction(ctx context.Context, work func(*sql.Tx) error) error {
	transaction, err := openedStore.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := work(transaction); err != nil {
		return errors.Join(err, transaction.Rollback())
	}
	return transaction.Commit()
}

func requireOneRow(result sql.Result, err error, notFound error, operation string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if affected == 0 {
		return notFound
	}
	return nil
}
