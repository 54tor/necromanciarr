package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"

	"necromanciarr/internal/dedupe"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
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
	changed, err := AssignedFilesChanged(deps.Registry, assignment.Files)
	if err != nil {
		return provenTorrent{}, "", err
	}
	if changed {
		return provenTorrent{}, ReasonChangedFile, nil
	}
	raw, err := deps.Store.CandidateTorrent(ctx, candidate.ID)
	if err != nil {
		return provenTorrent{}, "", err
	}
	torrent, parseErr := torrentcheck.Parse(raw)
	parsed := parseErr == nil
	if !parsed {
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

// AssignedFilesChanged compare, par le registre (nil : disque local), chaque fichier associé à son instantané de
// vérification (taille, date à la nanoseconde) ; un fichier absent compte comme changé. Seule une coupure du volume
// distant (fsys.ErrRemoteUnavailable) est une erreur : rien n'est conclu, la reprise se fera à la relance.
func AssignedFilesChanged(registry *fsys.Registry, files []store.AssignedFile) (bool, error) {
	for _, file := range files {
		info, statErr := statStored(registry, file.Path)
		if isRemoteUnavailable(statErr) {
			return false, statErr
		}
		isUnchanged := statErr == nil && info.Size() == file.Size && info.ModTime().Equal(file.ModTime)
		if !isUnchanged {
			return true, nil
		}
	}
	return false, nil
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

// annexPath est le chemin, dans le volume de linkRoot, de l'annexe sous le dossier du torrent.
func annexPath(torrent *torrentcheck.Torrent, linkRoot fsys.Location, annex store.VerifiedFile) (string, error) {
	relative, err := linker.RelativeDestination(torrent, annex.FileIndex)
	if err != nil {
		return "", err
	}
	return absoluteOf(linkRoot, relative), nil
}

// linkMatch dit si une destination porte le contenu de sa source, sous quelle sorte, et si l'identité du fichier le
// prouve (I4) : seulement le même fichier sur le disque local ; à distance, jamais, même avec une identité connue.
type linkMatch struct {
	kind           linker.Kind
	matches        bool
	identityProven bool
}

// entryMatches dit si destination porte déjà le contenu de source (chemins du volume de readFS) : même fichier (lien
// dur ou symbolique), ou fichier ordinaire de même taille et de même empreinte début-fin (un reflink ne se distingue
// pas d'une copie). Une destination absente ne correspond pas ; une coupure du volume distant est une erreur.
func entryMatches(ctx context.Context, deps Deps, readFS fsys.ReadFS, destination, source string) (linkMatch, error) {
	entryInfo, err := readFS.Lstat(destination)
	if errors.Is(err, fs.ErrNotExist) {
		return linkMatch{}, nil
	}
	if err != nil {
		return linkMatch{}, err
	}
	// Une source illisible ne prouve rien : la destination ne correspond pas.
	sourceInfo, sourceErr := readFS.Stat(source)
	if isRemoteUnavailable(sourceErr) {
		return linkMatch{}, sourceErr
	}
	if sourceReadable := sourceErr == nil; !sourceReadable {
		return linkMatch{}, nil
	}
	isSymlink := entryInfo.Mode()&fs.ModeSymlink != 0
	isLocal := readFS.Volume() == ""
	if sameFileByIdentity(readFS, destination, sourceInfo) {
		kind := linker.Hardlink
		if isSymlink {
			kind = linker.Symlink
		}
		return linkMatch{kind: kind, matches: true, identityProven: isLocal}, nil
	}
	if isSymlink && !isLocal {
		target, err := readFS.Readlink(destination)
		if isRemoteUnavailable(err) {
			return linkMatch{}, err
		}
		return linkMatch{kind: linker.Symlink, matches: err == nil && target == source}, nil
	}
	if !entryInfo.Mode().IsRegular() || entryInfo.Size() != sourceInfo.Size() {
		return linkMatch{}, nil
	}
	sameContent, err := sameHeadTail(ctx, deps, readFS, destination, entryInfo, source, sourceInfo)
	return linkMatch{kind: linker.Reflink, matches: sameContent}, err
}

// sameFileByIdentity : en local, même fichier au sens du système (comme os.SameFile) ; à distance, identités connues
// et égales, sinon rien n'est conclu.
func sameFileByIdentity(readFS fsys.ReadFS, destination string, sourceInfo fs.FileInfo) bool {
	resolved, err := readFS.Stat(destination)
	if err != nil {
		return false
	}
	if readFS.Volume() == "" {
		return local.SameFile(resolved, sourceInfo)
	}
	destinationID, destinationKnown := readFS.FileID(resolved)
	sourceID, sourceKnown := readFS.FileID(sourceInfo)
	return destinationKnown && sourceKnown && destinationID == sourceID
}

// sameHeadTail compare les empreintes début-fin, lues par readFS sous le limiteur de lecture ; celle de la
// destination est calculée sans cache : elle ne doit jamais emprunter celle d'un autre fichier.
func sameHeadTail(ctx context.Context, deps Deps, readFS fsys.ReadFS, destination string, destinationInfo fs.FileInfo, source string, sourceInfo fs.FileInfo) (bool, error) {
	volume := readFS.Volume()
	destinationPrint, err := dedupe.HeadTailFingerprintOf(ctx, nil, deps.ReadLimiter, openerOf(readFS, destination),
		storedIn(volume, destination), destinationInfo.Size(), destinationInfo.ModTime())
	if err != nil {
		return false, err
	}
	sourcePrint, err := dedupe.HeadTailFingerprintOf(ctx, deps.Store, deps.ReadLimiter, openerOf(readFS, source),
		storedIn(volume, source), sourceInfo.Size(), sourceInfo.ModTime())
	if err != nil {
		return false, err
	}
	return destinationPrint == sourcePrint, nil
}

func openerOf(readFS fsys.ReadFS, name string) func() (dedupe.ReadAtCloser, error) {
	return func() (dedupe.ReadAtCloser, error) { return readFS.Open(name) }
}

// annexMatches dit si path est un fichier ordinaire de la taille et du sha256 de l'annexe vérifiée ; seule une
// coupure du volume distant est une erreur.
func annexMatches(readFS fsys.ReadFS, path string, annex store.VerifiedFile) (bool, error) {
	info, err := readFS.Lstat(path)
	if isRemoteUnavailable(err) {
		return false, err
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() != annex.Size {
		return false, nil
	}
	file, err := readFS.Open(path)
	if isRemoteUnavailable(err) {
		return false, err
	}
	if err != nil {
		return false, nil
	}
	defer func() { _ = file.Close() }()
	hasher := sha256.New()
	// Un octet de plus : un fichier qui a grandi depuis le Lstat ne correspond plus.
	copied, err := io.Copy(hasher, io.NewSectionReader(file, 0, annex.Size+1))
	if isRemoteUnavailable(err) {
		return false, err
	}
	if err != nil || copied != annex.Size {
		return false, nil
	}
	return hex.EncodeToString(hasher.Sum(nil)) == annex.SHA256, nil
}
