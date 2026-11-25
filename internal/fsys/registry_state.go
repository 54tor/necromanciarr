package fsys

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"
)

// capabilitiesTimeout borne la sonde des capacités faite sous le verrou de chargement : WriteRoot n'a pas de
// contexte et un volume figé ne doit pas bloquer les autres appelants indéfiniment.
var capabilitiesTimeout = 30 * time.Second

// volumeEntry porte un volume et ses racines de bibliothèque ; l'état dérivé (formes canoniques, identités) est
// calculé au premier usage et gardé seulement s'il a réussi : un échec n'est pas mis en cache, l'appel suivant
// réessaie (volume revenu).
type volumeEntry struct {
	volume          Volume
	rawLibraryRoots []string
	foldCase        bool
	// writeRootCheck, s'il est posé, passe sur chaque racine d'écriture du volume avant qu'elle soit rendue.
	writeRootCheck func(WriteRoot) error

	loadMutex sync.Mutex
	state     *volumeState
}

// volumeState est immuable une fois chargé.
type volumeState struct {
	libraryKeys []string
	// useIdentity : le volume annonce des identités de dossier comparables (Capabilities.FileIdentity, RemoteStat).
	useIdentity     bool
	libraryIDs      map[FileID]bool
	libraryAncestry map[FileID]bool // identités de chaque racine et de tous ses ancêtres
	// identityGaps : chemins de bibliothèque (racine ou ancêtre) sans identité connue, comparés par le texte seul.
	identityGaps map[string]string
}

// identityUnknownReason : Stat a réussi mais le volume ne donne pas d'identité (inode nul, FUSE) pour ce chemin.
const identityUnknownReason = "file_identity_unknown"

// load tient le verrou pendant le chargement : les appelants concurrents patientent derrière le même essai au lieu de
// lancer chacun le leur.
func (entry *volumeEntry) load() (*volumeState, error) {
	entry.loadMutex.Lock()
	defer entry.loadMutex.Unlock()
	if entry.state != nil {
		return entry.state, nil
	}
	state, err := entry.buildState()
	if err != nil {
		return nil, err
	}
	entry.state = state
	return state, nil
}

func (entry *volumeEntry) buildState() (*volumeState, error) {
	state := &volumeState{libraryIDs: map[FileID]bool{}, libraryAncestry: map[FileID]bool{}}
	canonicalRoots := make([]string, 0, len(entry.rawLibraryRoots))
	for _, rawRoot := range entry.rawLibraryRoots {
		canonical, err := canonicalOf(entry.volume, rawRoot)
		if err != nil {
			return nil, err
		}
		if err := requireRealDirectory(entry.volume, canonical); err != nil {
			return nil, err
		}
		canonicalRoots = append(canonicalRoots, canonical)
		state.libraryKeys = append(state.libraryKeys, entry.comparisonKey(canonical))
	}
	if len(canonicalRoots) == 0 {
		return state, nil
	}
	probeContext, cancelProbe := context.WithTimeout(context.Background(), capabilitiesTimeout)
	defer cancelProbe()
	capabilities, err := entry.volume.Capabilities(probeContext)
	if err != nil {
		return nil, err
	}
	state.useIdentity = capabilities.FileIdentity || capabilities.RemoteStat
	if !state.useIdentity {
		return state, nil
	}
	return state, entry.recordLibraryIdentities(state, canonicalRoots)
}

func (entry *volumeEntry) recordLibraryIdentities(state *volumeState, canonicalRoots []string) error {
	state.identityGaps = map[string]string{}
	for _, canonicalRoot := range canonicalRoots {
		for ancestorIndex, ancestor := range ancestorsOf(entry.volume.Paths(), canonicalRoot) {
			ancestorID, known, err := entry.identityOf(ancestor)
			if err != nil {
				return err
			}
			if !known {
				state.identityGaps[ancestor] = identityUnknownReason
				continue
			}
			isRootItself := ancestorIndex == 0
			if isRootItself {
				state.libraryIDs[ancestorID] = true
			}
			state.libraryAncestry[ancestorID] = true
		}
	}
	return nil
}

// requireRealDirectory échoue fermé si la racine canonique n'est pas un vrai dossier : un realpath laxiste (lexical,
// hors OpenSSH) peut rendre un lien symbolique non résolu, dont le texte ne protégerait pas la bibliothèque visée.
func requireRealDirectory(volume Volume, canonical string) error {
	info, err := volume.Lstat(canonical)
	if err != nil {
		return &causeHidingError{sentinel: ErrCannotCanonicalize, cause: err}
	}
	isRealDirectory := info.IsDir() && info.Mode()&fs.ModeSymlink == 0
	if !isRealDirectory {
		return fmt.Errorf("%w: a library root is not a real directory", ErrCannotCanonicalize)
	}
	return nil
}

// overlapsLibrary compare d'abord les clés canoniques (plancher toujours appliqué), puis les identités : un
// ancêtre de candidate qui est une racine, ou candidate qui est une racine ou l'un de ses ancêtres.
func (entry *volumeEntry) overlapsLibrary(state *volumeState, canonical string) (bool, error) {
	syntax := entry.volume.Paths()
	candidateKey := entry.comparisonKey(canonical)
	for _, libraryKey := range state.libraryKeys {
		textOverlaps := isSameOrInside(syntax, candidateKey, libraryKey) || isSameOrInside(syntax, libraryKey, candidateKey)
		if textOverlaps {
			return true, nil
		}
	}
	if !state.useIdentity {
		return false, nil
	}
	for ancestorIndex, ancestor := range ancestorsOf(syntax, canonical) {
		ancestorID, known, err := entry.identityOf(ancestor)
		if err != nil {
			return false, err
		}
		if !known {
			continue
		}
		isCandidateItself := ancestorIndex == 0
		if state.libraryIDs[ancestorID] || (isCandidateItself && state.libraryAncestry[ancestorID]) {
			return true, nil
		}
	}
	return false, nil
}

// identityOf échoue fermé sur une erreur de Stat. Une identité inconnue alors que Stat a réussi rend
// known=false : ce chemin garde le plancher textuel seul, sans faire tomber le volume.
func (entry *volumeEntry) identityOf(name string) (FileID, bool, error) {
	info, err := entry.volume.Stat(name)
	if err != nil {
		return FileID{}, false, &causeHidingError{sentinel: ErrCannotCanonicalize, cause: err}
	}
	id, known := entry.volume.FileID(info)
	return id, known, nil
}

// comparisonKey est privée au registre : Canonical garde la forme sur disque (Root, pathMappings), seule la
// comparaison ignore la casse. strings.ToLower approche le pliage NTFS/APFS ; l'identité couvre le reste.
func (entry *volumeEntry) comparisonKey(canonical string) string {
	if entry.foldCase {
		return strings.ToLower(canonical)
	}
	return canonical
}

// ancestorsOf rend name puis chacun de ses parents jusqu'à la racine du volume.
func ancestorsOf(syntax PathSyntax, name string) []string {
	ancestors := []string{name}
	for current := name; ; {
		parent := syntax.Dir(current)
		if parent == current {
			return ancestors
		}
		ancestors = append(ancestors, parent)
		current = parent
	}
}

// isSameOrInside remonte candidate par Dir jusqu'à la racine du volume, sans comparer de préfixe textuel
// ("/srv/medi" n'est pas dans "/srv/media").
func isSameOrInside(syntax PathSyntax, candidate, ancestor string) bool {
	for _, current := range ancestorsOf(syntax, candidate) {
		if current == ancestor {
			return true
		}
	}
	return false
}
