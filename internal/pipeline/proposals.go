package pipeline

import (
	"context"
	"maps"
	"slices"
	"strconv"

	"necromanciarr/internal/i18n"
	"necromanciarr/internal/matcher"
	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// ProposalDetails est le contenu de Proposal.Details pour les propositions créées par Verify. MissingPieces compte les
// pièces non vérifiées ; Candidates liste chaque torrent d'un groupe en litige, sans en choisir aucun. Seedable et
// AnnounceSource (config, indexer ou none, jamais une URL) sont calculés à chaque lecture (WithAnnounce), jamais stockés,
// et absents pour une nature sans torrent (litige, dedupe, repair). Source est la source du candidat (provider ou
// torznab:<nom>, jamais une URL), posée à la lecture depuis le candidat.
type ProposalDetails struct {
	Percent           float64         `json:"percent"`
	VerifiedPieces    int             `json:"verified_pieces"`
	MissingPieces     int             `json:"missing_pieces"`
	TotalPieces       int             `json:"total_pieces"`
	MissingMediaFiles []string        `json:"missing_media_files,omitempty"`
	AmbiguousFiles    []AmbiguousFile `json:"ambiguous_files,omitempty"`
	ExcludedFiles     []ExcludedFile  `json:"excluded_files,omitempty"`
	Candidates        []GroupMember   `json:"candidates,omitempty"`
	Seedable          *bool           `json:"seedable,omitempty"`
	AnnounceSource    string          `json:"announce_source,omitempty"`
	Source            string          `json:"source"`
}

// ExclusionAnnexHashFailed : un fichier annexe de la bibliothèque, de même taille, a une pièce au SHA1 faux ; il n'est
// ni compté possédé ni lié, le client le téléchargera.
const ExclusionAnnexHashFailed = "annex_hash_failed"

// ExcludedFile est un fichier du torrent présent dans la bibliothèque mais écarté de l'association prouvée.
type ExcludedFile struct {
	TorrentPath string `json:"torrent_path"`
	Reason      string `json:"reason"`
	// Label est le libellé traduit de Reason, rempli à l'affichage par la couche applicative.
	Label string `json:"label,omitempty"`
}

// AmbiguousFile est un fichier du torrent que le hash n'a pas pu attribuer à un seul fichier local.
type AmbiguousFile struct {
	TorrentPath string   `json:"torrent_path"`
	LocalFiles  []string `json:"local_files"`
}

// GroupMember décrit un candidat d'un groupe en litige et la proposition qui le porte.
type GroupMember struct {
	CandidateID int64                `json:"candidate_id"`
	Name        string               `json:"name"`
	Tracker     string               `json:"tracker"`
	State       store.CandidateState `json:"state"`
	Kind        store.ProposalKind   `json:"kind,omitempty"`
	ProposalID  int64                `json:"proposal_id,omitempty"`
	Percent     float64              `json:"percent"`
}

// verdict est la proposition à créer pour un candidat vérifié ; kind vide = aucune (média non vérifié). ineligible
// nomme la raison qui rend le candidat failed ; excluded liste les index de fichiers du torrent écartés de l'association.
type verdict struct {
	kind       store.ProposalKind
	warnings   []i18n.Message
	details    ProposalDetails
	ineligible string
	excluded   map[int]bool
}

// classify range un candidat vérifié : un média présent dont une pièce a un SHA1 faux le rend inéligible ; une annexe
// présente dans ce cas est exclue ; puis un fichier ambigu est un litige (jamais départagé ici), un média absent de la
// bibliothèque un pack incomplet, 100 % des pièces un torrent complet, un média complet dont seules des pièces
// d'annexes absentes ou exclues manquent un partial_annex ; sinon aucune proposition.
func classify(torrent *torrentcheck.Torrent, assignment matcher.Assignment, source compositeSource, report torrentcheck.Report) verdict {
	suspects := suspectFiles(torrent, source, report.FailedPieces)
	for _, fileIndex := range suspects {
		if torrentcheck.IsMediaPath(torrent.Files[fileIndex].Path) {
			return verdict{ineligible: ReasonMediaHashFailed}
		}
	}
	decided := baseVerdict(torrent, assignment, report)
	decided.excluded = map[int]bool{}
	for _, fileIndex := range suspects {
		decided.excluded[fileIndex] = true
		decided.details.ExcludedFiles = append(decided.details.ExcludedFiles,
			ExcludedFile{TorrentPath: torrent.Files[fileIndex].Path, Reason: ExclusionAnnexHashFailed})
	}
	if len(suspects) > 0 && decided.kind != "" {
		decided.warnings = append(decided.warnings, countMessage(i18n.KeyWarningAnnexHashFailed, len(suspects)))
	}
	return decided
}

// countMessage construit un avertissement dont le seul paramètre est un compte.
func countMessage(key i18n.Key, count int) i18n.Message {
	return i18n.Message{Key: key, Params: map[string]string{"count": strconv.Itoa(count)}}
}

// suspectFiles liste, triés, les fichiers présents qui recouvrent une pièce au SHA1 faux : le hash ne dit pas lequel est
// faux, donc tous sont suspects.
func suspectFiles(torrent *torrentcheck.Torrent, source compositeSource, failedPieces []int) []int {
	suspect := map[int]bool{}
	for _, pieceIndex := range failedPieces {
		start := int64(pieceIndex) * torrent.PieceLength
		for _, fileIndex := range torrent.FilesOverlapping(start, start+torrent.PieceLength) {
			if _, present := source.ReaderAt(fileIndex); present {
				suspect[fileIndex] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(suspect))
}

func baseVerdict(torrent *torrentcheck.Torrent, assignment matcher.Assignment, report torrentcheck.Report) verdict {
	details := ProposalDetails{
		Percent: percentOf(report.VerifiedPieces, report.TotalPieces), VerifiedPieces: report.VerifiedPieces,
		MissingPieces: len(report.MissingPieces), TotalPieces: report.TotalPieces,
	}
	if len(assignment.Ambiguous) > 0 {
		details.AmbiguousFiles = ambiguousFiles(torrent, assignment)
		return verdict{kind: store.ProposalConflict, details: details}
	}
	if missing := missingMediaFiles(torrent, assignment); len(missing) > 0 {
		details.MissingMediaFiles = missing
		warning := countMessage(i18n.KeyWarningPackMediaAbsent, len(missing))
		return verdict{kind: store.ProposalPartialPack, warnings: []i18n.Message{warning}, details: details}
	}
	if report.TotalPieces > 0 && len(report.MissingPieces) == 0 {
		return verdict{kind: store.ProposalFull, details: details}
	}
	if report.MediaComplete {
		warning := countMessage(i18n.KeyWarningAnnexPiecesNotProven, len(report.MissingPieces))
		return verdict{kind: store.ProposalPartialAnnex, warnings: []i18n.Message{warning}, details: details}
	}
	return verdict{}
}

func ambiguousFiles(torrent *torrentcheck.Torrent, assignment matcher.Assignment) []AmbiguousFile {
	var files []AmbiguousFile
	for _, fileIndex := range slices.Sorted(maps.Keys(assignment.Ambiguous)) {
		ambiguous := AmbiguousFile{TorrentPath: torrent.Files[fileIndex].Path}
		for _, local := range assignment.Ambiguous[fileIndex] {
			ambiguous.LocalFiles = append(ambiguous.LocalFiles, local.Path)
		}
		files = append(files, ambiguous)
	}
	return files
}

func missingMediaFiles(torrent *torrentcheck.Torrent, assignment matcher.Assignment) []string {
	var missing []string
	for fileIndex, file := range torrent.Files {
		_, assigned := assignment.ByTorrentFile[fileIndex]
		if file.Size > 0 && torrentcheck.IsMediaPath(file.Path) && !assigned {
			missing = append(missing, file.Path)
		}
	}
	return missing
}

// GroupOf donne le groupe du candidat ; un candidat jamais regroupé forme son propre groupe.
func GroupOf(candidate store.Candidate) int64 {
	if candidate.GroupID != 0 {
		return candidate.GroupID
	}
	return candidate.ID
}

// propose crée (ou met à jour si elle attend encore) la proposition du candidat vérifié.
func (run candidateRun) propose(ctx context.Context, candidate store.Candidate, decided verdict) error {
	switch decided.kind {
	case "":
		run.result.count(ReasonMediaNotVerified, 1)
		return nil
	case store.ProposalPartialAnnex:
		run.result.PartialAnnex++
	case store.ProposalPartialPack:
		run.result.PartialPack++
	}
	if run.deps.aboveCap(decided.kind, decided.details) {
		return holdVerdict(ctx, run.deps, candidate.ID, decided.kind, decided.warnings, decided.details)
	}
	if err := run.deps.Store.DeleteCandidateHold(ctx, candidate.ID); err != nil {
		return err
	}
	created, err := upsertProposal(ctx, run.deps, store.Proposal{
		CandidateID: candidate.ID, GroupID: GroupOf(candidate), Kind: decided.kind, Warnings: decided.warnings,
	}, decided.details)
	if created && decided.kind == store.ProposalConflict {
		run.result.Conflicts++
	}
	if created {
		run.result.ProposalsCreated++
	}
	if IsTorrentProposal(decided.kind) && AnnounceSourceOf(run.deps.Announces, candidate) == sources.AnnounceNone {
		run.result.count(ReasonNoAnnounce, 1)
	}
	return err
}

func upsertProposal(ctx context.Context, deps Deps, proposal store.Proposal, details ProposalDetails) (bool, error) {
	encoded, err := encodeStoredDetails(details)
	if err != nil {
		return false, err
	}
	proposal.Details = encoded
	_, created, err := deps.Store.UpsertProposal(ctx, proposal, deps.now())
	return created, err
}
