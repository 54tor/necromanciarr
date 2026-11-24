package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// VerificationState est l'avancement de la vérification des pièces d'un candidat.
type VerificationState string

// États d'une vérification : en cours (reprise possible), terminée, échouée.
const (
	VerificationInProgress VerificationState = "in_progress"
	VerificationComplete   VerificationState = "complete"
	VerificationFailed     VerificationState = "failed"
)

// MaxVerifiedFileBytes plafonne une annexe vérifiée conservée en base.
const MaxVerifiedFileBytes = 1 << 20

// Erreurs typées des vérifications et des annexes.
var (
	ErrUnknownVerificationState = errors.New("store: unknown verification state")
	ErrVerifiedFileTooLarge     = errors.New("store: verified file exceeds 1 MiB")
)

// Verification est le résultat, éventuellement incomplet, du contrôle des pièces d'un candidat. CheckedBitset marque les
// pièces déjà contrôlées (reprise sans les relire), VerifiedBitset celles dont le SHA1 concorde ; bit i = octet i/8,
// bit de poids faible i%8. Une pièce contrôlée et non vérifiée est manquante. Generation est celle du candidat au moment
// de l'écriture (posée par le store) ; MappingFingerprint n'est rempli que par CompleteVerification.
type Verification struct {
	CandidateID        int64
	TotalPieces        int
	VerifiedPieces     int
	CheckedBitset      []byte
	VerifiedBitset     []byte
	MediaComplete      bool
	Percent            float64
	State              VerificationState
	Generation         int64
	MappingFingerprint string
	UpdatedAt          time.Time
}

func checkVerificationState(state VerificationState) error {
	switch state {
	case VerificationInProgress, VerificationComplete, VerificationFailed:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnknownVerificationState, state)
	}
}

// SaveVerification enregistre ou remplace la vérification du candidat, sans empreinte d'association.
func (openedStore *Store) SaveVerification(ctx context.Context, verification Verification, now time.Time) error {
	if err := checkVerificationState(verification.State); err != nil {
		return err
	}
	verification.MappingFingerprint = ""
	if err := writeVerification(ctx, openedStore.database, verification, now); err != nil {
		return fmt.Errorf("save verification: %w", err)
	}
	return nil
}

func writeVerification(ctx context.Context, database executor, verification Verification, now time.Time) error {
	_, err := database.ExecContext(ctx, `INSERT INTO verifications
	(candidate_id, total_pieces, verified_pieces, checked_bitset, verified_bitset, media_complete, percent, state, updated_at,
	generation, mapping_fingerprint)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, (SELECT generation FROM torrent_candidates WHERE id = ?), ?)
ON CONFLICT(candidate_id) DO UPDATE SET total_pieces = excluded.total_pieces, verified_pieces = excluded.verified_pieces,
	checked_bitset = excluded.checked_bitset, verified_bitset = excluded.verified_bitset,
	media_complete = excluded.media_complete, percent = excluded.percent, state = excluded.state, updated_at = excluded.updated_at,
	generation = excluded.generation, mapping_fingerprint = excluded.mapping_fingerprint`,
		verification.CandidateID, verification.TotalPieces, verification.VerifiedPieces, nonNilBytes(verification.CheckedBitset),
		nonNilBytes(verification.VerifiedBitset), verification.MediaComplete, verification.Percent, string(verification.State), now.UnixNano(),
		verification.CandidateID, verification.MappingFingerprint)
	return err
}

// LoadVerification renvoie la vérification du candidat ; found est faux s'il n'en a pas (ou si elle a été invalidée).
func (openedStore *Store) LoadVerification(ctx context.Context, candidateID int64) (verification Verification, found bool, err error) {
	var state string
	var updatedAt int64
	err = openedStore.database.QueryRowContext(ctx, `SELECT total_pieces, verified_pieces, checked_bitset, verified_bitset,
	media_complete, percent, state, updated_at, generation, mapping_fingerprint FROM verifications WHERE candidate_id = ?`, candidateID).
		Scan(&verification.TotalPieces, &verification.VerifiedPieces, &verification.CheckedBitset, &verification.VerifiedBitset,
			&verification.MediaComplete, &verification.Percent, &state, &updatedAt, &verification.Generation, &verification.MappingFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return Verification{}, false, nil
	}
	if err != nil {
		return Verification{}, false, fmt.Errorf("load verification: %w", err)
	}
	verification.CandidateID, verification.State = candidateID, VerificationState(state)
	verification.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return verification, true, nil
}

// nonNilBytes évite d'écrire NULL dans une colonne BLOB NOT NULL.
func nonNilBytes(raw []byte) []byte {
	if raw == nil {
		return []byte{}
	}
	return raw
}

// VerifiedFile est une annexe du torrent (un .nfo, typiquement) dont le contenu a passé le SHA1 des pièces ; Size et
// SHA256 sont calculés par SaveVerifiedFile.
type VerifiedFile struct {
	CandidateID int64
	FileIndex   int
	Path        string
	Size        int64
	SHA256      string
	Data        []byte
}

// SaveVerifiedFile conserve l'annexe vérifiée (au plus MaxVerifiedFileBytes), en remplaçant celle de même index.
func (openedStore *Store) SaveVerifiedFile(ctx context.Context, file VerifiedFile) error {
	if len(file.Data) > MaxVerifiedFileBytes {
		return ErrVerifiedFileTooLarge
	}
	digest := sha256.Sum256(file.Data)
	_, err := openedStore.database.ExecContext(ctx, `INSERT INTO verified_files (candidate_id, file_index, path, size, sha256, data)
	VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(candidate_id, file_index) DO UPDATE SET path = excluded.path, size = excluded.size, sha256 = excluded.sha256,
	data = excluded.data`,
		file.CandidateID, file.FileIndex, file.Path, len(file.Data), hex.EncodeToString(digest[:]), nonNilBytes(file.Data))
	if err != nil {
		return fmt.Errorf("save verified file: %w", err)
	}
	return nil
}

// ListVerifiedFiles renvoie les annexes vérifiées du candidat, triées par index de fichier.
func (openedStore *Store) ListVerifiedFiles(ctx context.Context, candidateID int64) ([]VerifiedFile, error) {
	rows, err := openedStore.database.QueryContext(ctx,
		`SELECT file_index, path, size, sha256, data FROM verified_files WHERE candidate_id = ? ORDER BY file_index`, candidateID)
	if err != nil {
		return nil, fmt.Errorf("list verified files: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var files []VerifiedFile
	for rows.Next() {
		file := VerifiedFile{CandidateID: candidateID}
		if err := rows.Scan(&file.FileIndex, &file.Path, &file.Size, &file.SHA256, &file.Data); err != nil {
			return nil, fmt.Errorf("scan verified file: %w", err)
		}
		files = append(files, file)
	}
	return files, rows.Err()
}

// ProposalKind est la nature d'une proposition soumise à l'opérateur.
type ProposalKind string

// Natures d'une proposition.
const (
	ProposalFull         ProposalKind = "full"
	ProposalPartialAnnex ProposalKind = "partial_annex"
	ProposalPartialPack  ProposalKind = "partial_pack"
	ProposalCrossSeed    ProposalKind = "cross_seed"
	ProposalConflict     ProposalKind = "conflict"
	ProposalDedupe       ProposalKind = "dedupe"
	ProposalRepair       ProposalKind = "repair"
)

// ProposalDecision est la décision de l'opérateur sur une proposition, puis son application.
type ProposalDecision string

// Décisions d'une proposition : pending → accepted | rejected | superseded ; accepted → applied | failed | superseded ;
// rejected → superseded ; failed → pending seulement par RetryProposal.
const (
	ProposalPending  ProposalDecision = "pending"
	ProposalAccepted ProposalDecision = "accepted"
	ProposalRejected ProposalDecision = "rejected"
	ProposalApplied  ProposalDecision = "applied"
	ProposalFailed   ProposalDecision = "failed"
	// ProposalSuperseded clôt une proposition ouverte dont le candidat a été périmé ou réassocié.
	ProposalSuperseded ProposalDecision = "superseded"
)

// Erreurs typées des propositions.
var (
	ErrUnknownProposalKind     = errors.New("store: unknown proposal kind")
	ErrUnknownProposalDecision = errors.New("store: unknown proposal decision")
	ErrProposalNotFound        = errors.New("store: proposal not found")
	// ErrInvalidProposalDetails signale des détails qui ne sont pas un objet JSON.
	ErrInvalidProposalDetails = errors.New("store: proposal details must be a JSON object")
	// ErrUnknownPrimarySource signale une origine de proposition principale inconnue.
	ErrUnknownPrimarySource = errors.New("store: unknown primary source")
	// ErrProposalNeedsCandidate signale une proposition sans candidat passée à UpsertProposal.
	ErrProposalNeedsCandidate = errors.New("store: proposal needs a candidate")
)

// Proposal est une action soumise à l'opérateur ; CandidateID vaut 0 pour une proposition sans torrent (dedupe,
// repair) et DecidedAt est zéro tant qu'aucune décision n'est prise. Primary marque la proposition full retenue dans
// son groupe (une seule ouverte à la fois, garanti par la base), PrimarySource dit qui l'a choisie ; Details est un
// objet JSON propre à la nature (pourcentage, pièces manquantes, torrents en litige), vide vaut {}. Generation est
// celle du candidat à la création (posée par UpsertProposal).
type Proposal struct {
	ID            int64
	CandidateID   int64
	GroupID       int64
	Kind          ProposalKind
	Decision      ProposalDecision
	Warnings      []string
	Primary       bool
	PrimarySource PrimarySource
	Generation    int64
	Details       json.RawMessage
	CreatedAt     time.Time
	DecidedAt     time.Time
}

func checkProposalKind(kind ProposalKind) error {
	switch kind {
	case ProposalFull, ProposalPartialAnnex, ProposalPartialPack, ProposalCrossSeed, ProposalConflict, ProposalDedupe, ProposalRepair:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnknownProposalKind, kind)
	}
}

func checkProposalDecision(decision ProposalDecision) error {
	switch decision {
	case ProposalPending, ProposalAccepted, ProposalRejected, ProposalApplied, ProposalFailed, ProposalSuperseded:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnknownProposalDecision, decision)
	}
}

// CreateProposal enregistre une proposition en attente et renvoie son identifiant.
func (openedStore *Store) CreateProposal(ctx context.Context, proposal Proposal, now time.Time) (int64, error) {
	encoded, err := encodeProposal(proposal)
	if err != nil {
		return 0, err
	}
	return insertProposal(ctx, openedStore.database, proposal, encoded, now)
}

// encodedProposal porte les colonnes JSON d'une proposition validée.
type encodedProposal struct {
	warnings, details string
}

func encodeProposal(proposal Proposal) (encodedProposal, error) {
	if err := checkProposalKind(proposal.Kind); err != nil {
		return encodedProposal{}, err
	}
	warnings, err := json.Marshal(nonNilStrings(proposal.Warnings))
	if err != nil {
		return encodedProposal{}, fmt.Errorf("encode proposal warnings: %w", err)
	}
	details, err := normalizeDetails(proposal.Details)
	if err != nil {
		return encodedProposal{}, err
	}
	return encodedProposal{warnings: string(warnings), details: details}, nil
}

func normalizeDetails(details json.RawMessage) (string, error) {
	if len(details) == 0 {
		return "{}", nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(details, &object); err != nil || object == nil {
		return "", ErrInvalidProposalDetails
	}
	return string(details), nil
}

func insertProposal(ctx context.Context, database executor, proposal Proposal, encoded encodedProposal, now time.Time) (int64, error) {
	result, err := database.ExecContext(ctx, `INSERT INTO proposals
	(candidate_id, group_id, kind, decision, warnings, details, created_at, generation)
	VALUES (?, ?, ?, ?, ?, ?, ?, COALESCE((SELECT generation FROM torrent_candidates WHERE id = ?), 0))`,
		nullableID(proposal.CandidateID), nullableID(proposal.GroupID), string(proposal.Kind),
		string(ProposalPending), encoded.warnings, encoded.details, now.UnixNano(), proposal.CandidateID)
	if err != nil {
		return 0, fmt.Errorf("create proposal: %w", err)
	}
	return result.LastInsertId()
}

// PrimarySource dit qui a choisi la proposition principale d'un groupe.
type PrimarySource string

// Provenances d'une proposition principale : élue par le pipeline (réélue quand un résultat change) ou choisie par
// l'opérateur (gardée tant que son candidat n'est pas périmé).
const (
	PrimaryAuto     PrimarySource = "auto"
	PrimaryOperator PrimarySource = "operator"
)

// UpsertProposal crée au plus une proposition non close (toute décision sauf superseded) par candidat, nature et
// génération du candidat : une proposition rejetée ou appliquée d'une génération antérieure ne bloque pas la nouvelle.
// Si elle existe et attend encore, ses avertissements et ses détails sont mis à jour, et son groupe aussi (un
// changement de groupe lui retire le rôle de principale) ; décidée, elle reste telle quelle. created dit si une ligne a
// été insérée. Rejouable.
func (openedStore *Store) UpsertProposal(ctx context.Context, proposal Proposal, now time.Time) (proposalID int64, created bool, err error) {
	if proposal.CandidateID == 0 {
		return 0, false, ErrProposalNeedsCandidate
	}
	encoded, err := encodeProposal(proposal)
	if err != nil {
		return 0, false, err
	}
	err = openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		var upsertErr error
		proposalID, created, upsertErr = upsertProposal(ctx, transaction, proposal, encoded, now)
		return upsertErr
	})
	if err != nil {
		return 0, false, fmt.Errorf("upsert proposal: %w", err)
	}
	return proposalID, created, nil
}

func upsertProposal(ctx context.Context, transaction *sql.Tx, proposal Proposal, encoded encodedProposal, now time.Time) (int64, bool, error) {
	var generation int64
	if err := transaction.QueryRowContext(ctx, `SELECT generation FROM torrent_candidates WHERE id = ?`, proposal.CandidateID).
		Scan(&generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, ErrCandidateNotFound
		}
		return 0, false, err
	}
	var proposalID int64
	var existingDecision string
	lookupErr := transaction.QueryRowContext(ctx, `SELECT id, decision FROM proposals
WHERE candidate_id = ? AND kind = ? AND generation = ? AND decision != ? ORDER BY id LIMIT 1`,
		proposal.CandidateID, string(proposal.Kind), generation, string(ProposalSuperseded)).Scan(&proposalID, &existingDecision)
	if errors.Is(lookupErr, sql.ErrNoRows) {
		proposalID, err := insertProposal(ctx, transaction, proposal, encoded, now)
		return proposalID, err == nil, err
	}
	if lookupErr != nil || ProposalDecision(existingDecision) != ProposalPending {
		return proposalID, false, lookupErr
	}
	if err := moveProposalToGroup(ctx, transaction, proposalID, proposal.GroupID); err != nil {
		return 0, false, err
	}
	_, err := transaction.ExecContext(ctx, `UPDATE proposals SET warnings = ?, details = ? WHERE id = ?`,
		encoded.warnings, encoded.details, proposalID)
	return proposalID, false, err
}

// moveProposalToGroup rattache la proposition au groupe ; si le groupe change, elle perd le rôle de principale (une
// fusion de groupes est suivie d'une nouvelle élection).
func moveProposalToGroup(ctx context.Context, transaction *sql.Tx, proposalID, groupID int64) error {
	_, err := transaction.ExecContext(ctx, `UPDATE proposals SET is_primary = 0, primary_source = '', group_id = ?
WHERE id = ? AND group_id IS NOT ?`, nullableID(groupID), proposalID, nullableID(groupID))
	return err
}

// SetProposalGroup rattache une proposition à un autre groupe (après un regroupement) ; elle cesse d'y être principale.
func (openedStore *Store) SetProposalGroup(ctx context.Context, proposalID, groupID int64) error {
	return openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		return moveProposalToGroup(ctx, transaction, proposalID, groupID)
	})
}

// SetGroupPrimary fait de la proposition la seule principale du groupe (elle y est rattachée), avec son origine ; elle
// doit être full, pending ou accepted, sinon ErrInvalidTransition.
func (openedStore *Store) SetGroupPrimary(ctx context.Context, groupID, proposalID int64, source PrimarySource) error {
	if source != PrimaryAuto && source != PrimaryOperator {
		return fmt.Errorf("%w: %q", ErrUnknownPrimarySource, source)
	}
	return openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		var kind, decision string
		err := transaction.QueryRowContext(ctx, `SELECT kind, decision FROM proposals WHERE id = ?`, proposalID).Scan(&kind, &decision)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrProposalNotFound
		}
		if err != nil {
			return fmt.Errorf("load proposal: %w", err)
		}
		isOpen := ProposalDecision(decision) == ProposalPending || ProposalDecision(decision) == ProposalAccepted
		if ProposalKind(kind) != ProposalFull || !isOpen {
			return fmt.Errorf("%w: %s %s proposal cannot be primary", ErrInvalidTransition, decision, kind)
		}
		if err := clearGroupPrimary(ctx, transaction, groupID); err != nil {
			return err
		}
		_, err = transaction.ExecContext(ctx, `UPDATE proposals SET is_primary = 1, primary_source = ?, group_id = ? WHERE id = ?`,
			string(source), groupID, proposalID)
		return err
	})
}

// ClearGroupPrimary retire le rôle de principale à toute proposition du groupe (groupe sans torrent complet).
func (openedStore *Store) ClearGroupPrimary(ctx context.Context, groupID int64) error {
	return openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		return clearGroupPrimary(ctx, transaction, groupID)
	})
}

func clearGroupPrimary(ctx context.Context, transaction *sql.Tx, groupID int64) error {
	_, err := transaction.ExecContext(ctx, `UPDATE proposals SET is_primary = 0, primary_source = '' WHERE group_id = ? AND is_primary = 1`, groupID)
	if err != nil {
		return fmt.Errorf("clear group primary: %w", err)
	}
	return nil
}

// ListProposals renvoie les propositions de la décision donnée (toutes si vide), triées par identifiant.
func (openedStore *Store) ListProposals(ctx context.Context, decision ProposalDecision) ([]Proposal, error) {
	rows, err := openedStore.database.QueryContext(ctx, `SELECT id, candidate_id, group_id, kind, decision, warnings, is_primary,
	primary_source, generation, details, created_at, decided_at FROM proposals WHERE ? = '' OR decision = ? ORDER BY id`, string(decision), string(decision))
	if err != nil {
		return nil, fmt.Errorf("list proposals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var proposals []Proposal
	for rows.Next() {
		proposal, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		proposals = append(proposals, proposal)
	}
	return proposals, rows.Err()
}

func scanProposal(rows *sql.Rows) (Proposal, error) {
	var proposal Proposal
	var candidateID, groupID, decidedAt sql.NullInt64
	var kind, decision, warnings, details, primarySource string
	var createdAt int64
	if err := rows.Scan(&proposal.ID, &candidateID, &groupID, &kind, &decision, &warnings, &proposal.Primary, &primarySource,
		&proposal.Generation, &details, &createdAt, &decidedAt); err != nil {
		return Proposal{}, fmt.Errorf("scan proposal: %w", err)
	}
	if err := json.Unmarshal([]byte(warnings), &proposal.Warnings); err != nil {
		return Proposal{}, fmt.Errorf("decode proposal warnings: %w", err)
	}
	proposal.Details, proposal.PrimarySource = json.RawMessage(details), PrimarySource(primarySource)
	proposal.CandidateID, proposal.GroupID = candidateID.Int64, groupID.Int64
	proposal.Kind, proposal.Decision = ProposalKind(kind), ProposalDecision(decision)
	proposal.CreatedAt = time.Unix(0, createdAt).UTC()
	if decidedAt.Valid {
		proposal.DecidedAt = time.Unix(0, decidedAt.Int64).UTC()
	}
	return proposal, nil
}

// proposalTransitions liste les décisions atteignables par SetProposalDecision depuis chaque décision.
var proposalTransitions = map[ProposalDecision][]ProposalDecision{
	ProposalPending:  {ProposalAccepted, ProposalRejected, ProposalSuperseded},
	ProposalAccepted: {ProposalApplied, ProposalFailed, ProposalSuperseded},
	// Un litige rejeté est clos puis recréé quand la composition de son groupe change.
	ProposalRejected: {ProposalSuperseded},
}

// SetProposalDecision enregistre la décision et sa date si la transition est autorisée ; accepted et applied exigent un
// candidat verified (une proposition sans candidat, dedupe ou repair, en est exemptée). Sinon ErrInvalidTransition.
func (openedStore *Store) SetProposalDecision(ctx context.Context, proposalID int64, decision ProposalDecision, now time.Time) error {
	if err := checkProposalDecision(decision); err != nil {
		return err
	}
	return openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		current, candidateState, err := proposalState(ctx, transaction, proposalID)
		if err != nil {
			return err
		}
		if !slices.Contains(proposalTransitions[current], decision) {
			return fmt.Errorf("%w: proposal %s -> %s", ErrInvalidTransition, current, decision)
		}
		requiresVerified := decision == ProposalAccepted || decision == ProposalApplied
		if requiresVerified && candidateState.Valid && CandidateState(candidateState.String) != CandidateVerified {
			return fmt.Errorf("%w: proposal %s needs a verified candidate, got %s", ErrInvalidTransition, decision, candidateState.String)
		}
		return writeDecision(ctx, transaction, proposalID, decision, now)
	})
}

// RetryProposal remet en attente une proposition dont l'application a échoué ; son candidat doit être verified (une
// proposition sans candidat en est exemptée). Sinon ErrInvalidTransition.
func (openedStore *Store) RetryProposal(ctx context.Context, proposalID int64, now time.Time) error {
	return openedStore.inTransaction(ctx, func(transaction *sql.Tx) error {
		current, candidateState, err := proposalState(ctx, transaction, proposalID)
		if err != nil {
			return err
		}
		if current != ProposalFailed {
			return fmt.Errorf("%w: retry from %s", ErrInvalidTransition, current)
		}
		if candidateState.Valid && CandidateState(candidateState.String) != CandidateVerified {
			return fmt.Errorf("%w: retry needs a verified candidate, got %s", ErrInvalidTransition, candidateState.String)
		}
		return writeDecision(ctx, transaction, proposalID, ProposalPending, now)
	})
}

// proposalState renvoie la décision courante et l'état du candidat (non valide si la proposition n'en a pas).
func proposalState(ctx context.Context, transaction *sql.Tx, proposalID int64) (ProposalDecision, sql.NullString, error) {
	var decision string
	var candidateState sql.NullString
	err := transaction.QueryRowContext(ctx, `SELECT proposals.decision, torrent_candidates.state FROM proposals
LEFT JOIN torrent_candidates ON torrent_candidates.id = proposals.candidate_id WHERE proposals.id = ?`, proposalID).
		Scan(&decision, &candidateState)
	if errors.Is(err, sql.ErrNoRows) {
		return "", sql.NullString{}, ErrProposalNotFound
	}
	if err != nil {
		return "", sql.NullString{}, fmt.Errorf("load proposal: %w", err)
	}
	return ProposalDecision(decision), candidateState, nil
}

func writeDecision(ctx context.Context, transaction *sql.Tx, proposalID int64, decision ProposalDecision, now time.Time) error {
	_, err := transaction.ExecContext(ctx, `UPDATE proposals SET decision = ?, decided_at = ? WHERE id = ?`,
		string(decision), now.UnixNano(), proposalID)
	if err != nil {
		return fmt.Errorf("set proposal decision: %w", err)
	}
	return nil
}

func nullableID(identifier int64) sql.NullInt64 {
	return sql.NullInt64{Int64: identifier, Valid: identifier != 0}
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
