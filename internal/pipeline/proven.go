package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/matcher"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// provenTorrent est ce que Link pose et ce que Seed revérifie : le torrent, l'association prouvée (sans les fichiers
// écartés) et les annexes vérifiées.
type provenTorrent struct {
	torrent    *torrentcheck.Torrent
	assignment matcher.Assignment
	annexes    []store.VerifiedFile
}

// loadProven relit l'association prouvée et le .torrent ; une raison non vide est un refus (changed_file_stale : un
// fichier source a changé depuis la vérification, le candidat doit passer à stale).
func loadProven(ctx context.Context, deps Deps, proposal store.Proposal, candidate store.Candidate) (provenTorrent, string, error) {
	assignment, found, err := deps.Store.LoadVerifiedAssignment(ctx, candidate.ID)
	switch {
	case errors.Is(err, store.ErrAssignmentMismatch):
		return provenTorrent{}, ReasonAssignmentMismatch, nil
	case err != nil:
		return provenTorrent{}, "", err
	case !found:
		return provenTorrent{}, ReasonNoVerifiedAssignment, nil
	}
	if AssignedFilesChanged(assignment.Files) {
		return provenTorrent{}, ReasonChangedFile, nil
	}
	raw, err := deps.Store.CandidateTorrent(ctx, candidate.ID)
	if err != nil {
		return provenTorrent{}, "", err
	}
	torrent, parseErr := torrentcheck.Parse(raw)
	if parseErr != nil {
		return provenTorrent{}, ReasonInvalidTorrent, nil
	}
	annexes, err := deps.Store.ListVerifiedFiles(ctx, candidate.ID)
	if err != nil {
		return provenTorrent{}, "", err
	}
	proven := provenTorrent{torrent: torrent, assignment: provenAssignment(torrent, assignment, excludedPaths(proposal)), annexes: annexes}
	if len(proven.assignment.ByTorrentFile) == 0 {
		return provenTorrent{}, ReasonNoVerifiedAssignment, nil
	}
	return proven, "", nil
}

// AssignedFilesChanged compare chaque fichier associé à son instantané de vérification (taille, date à la
// nanoseconde) ; un fichier absent compte comme changé.
func AssignedFilesChanged(files []store.AssignedFile) bool {
	for _, file := range files {
		info, err := os.Stat(file.Path)
		if err != nil || info.Size() != file.Size || !info.ModTime().Equal(file.ModTime) {
			return true
		}
	}
	return false
}

func excludedPaths(proposal store.Proposal) map[string]bool {
	var details ProposalDetails
	excluded := map[string]bool{}
	if json.Unmarshal(proposal.Details, &details) != nil {
		return excluded
	}
	for _, file := range details.ExcludedFiles {
		excluded[file.TorrentPath] = true
	}
	return excluded
}

// provenAssignment reprend exactement l'association prouvée (jamais de nouvel appel à matcher.Assign), sans les
// fichiers écartés par Verify.
func provenAssignment(torrent *torrentcheck.Torrent, assignment store.VerifiedAssignment, excluded map[string]bool) matcher.Assignment {
	proven := matcher.Assignment{ByTorrentFile: map[int]store.MediaFile{}}
	for _, file := range assignment.Files {
		isKnownIndex := file.TorrentFileIndex >= 0 && file.TorrentFileIndex < len(torrent.Files)
		if isKnownIndex && excluded[torrent.Files[file.TorrentFileIndex].Path] {
			continue
		}
		proven.ByTorrentFile[file.TorrentFileIndex] = store.MediaFile{ID: file.MediaFileID, Path: file.Path, Size: file.Size, ModTime: file.ModTime}
	}
	return proven
}

// annexPath est le chemin de l'annexe sous le dossier du torrent.
func annexPath(torrent *torrentcheck.Torrent, linkRoot string, annex store.VerifiedFile) (string, error) {
	relative, err := linker.RelativeDestination(torrent, annex.FileIndex)
	if err != nil {
		return "", err
	}
	return filepath.Join(linkRoot, filepath.FromSlash(relative)), nil
}

// entryMatches dit si destination porte déjà le contenu de source, et sous quelle sorte : même fichier (lien dur ou
// symbolique), ou fichier ordinaire de même taille et de même empreinte début-fin (un reflink ne se distingue pas
// d'une copie). Une destination absente ne correspond pas.
func entryMatches(ctx context.Context, deps Deps, destination, source string) (linker.Kind, bool, error) {
	entryInfo, err := os.Lstat(destination)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	// Une source illisible ne prouve rien : la destination ne correspond pas.
	sourceInfo, sourceErr := os.Stat(source)
	sourceReadable := sourceErr == nil
	if !sourceReadable {
		return 0, false, nil
	}
	if resolved, statErr := os.Stat(destination); statErr == nil && os.SameFile(resolved, sourceInfo) {
		if entryInfo.Mode()&fs.ModeSymlink != 0 {
			return linker.Symlink, true, nil
		}
		return linker.Hardlink, true, nil
	}
	if !entryInfo.Mode().IsRegular() || entryInfo.Size() != sourceInfo.Size() {
		return 0, false, nil
	}
	// Empreinte de la destination sans cache : elle ne doit jamais emprunter celle d'un autre fichier.
	destinationPrint, _, err := dedupe.HeadTailFingerprint(ctx, nil, deps.ReadLimiter, destination, entryInfo.Size(), entryInfo.ModTime(), true)
	if err != nil {
		return 0, false, err
	}
	sourcePrint, _, err := dedupe.HeadTailFingerprint(ctx, deps.Store, deps.ReadLimiter, source, sourceInfo.Size(), sourceInfo.ModTime(), true)
	if err != nil {
		return 0, false, err
	}
	return linker.Reflink, destinationPrint == sourcePrint, nil
}

// annexMatches dit si path est un fichier ordinaire de la taille et du sha256 de l'annexe vérifiée.
func annexMatches(path string, annex store.VerifiedFile) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != annex.Size {
		return false
	}
	content, err := os.ReadFile(path) // #nosec G304 -- chemin sous le dossier de liens du torrent
	if err != nil {
		return false
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]) == annex.SHA256
}
