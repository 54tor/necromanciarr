package fsys

import (
	"context"
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

	loadMutex sync.Mutex
	state     *volumeState
}

// volumeState est immuable une fois chargé.
type volumeState struct {
	libraryKeys []string
	// useIdentity : le volume fournit des identités de fichier fiables (Capabilities).
	useIdentity     bool
	libraryIDs      map[FileID]bool
	libraryAncestry map[FileID]bool // identités de chaque racine et de tous ses ancêtres
}

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
	for _, canonicalRoot := range canonicalRoots {
		rootID, err := entry.identityOf(canonicalRoot)
		if err != nil {
			return err
		}
		state.libraryIDs[rootID] = true
		for _, ancestor := range ancestorsOf(entry.volume.Paths(), canonicalRoot) {
			ancestorID, err := entry.identityOf(ancestor)
			if err != nil {
				return err
			}
			state.libraryAncestry[ancestorID] = true
		}
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
		ancestorID, err := entry.identityOf(ancestor)
		if err != nil {
			return false, err
		}
		isCandidateItself := ancestorIndex == 0
		if state.libraryIDs[ancestorID] || (isCandidateItself && state.libraryAncestry[ancestorID]) {
			return true, nil
		}
	}
	return false, nil
}

// identityOf échoue fermé : une identité promise mais absente refuse l'écriture.
func (entry *volumeEntry) identityOf(name string) (FileID, error) {
	info, err := entry.volume.Stat(name)
	if err != nil {
		return FileID{}, &causeHidingError{sentinel: ErrCannotCanonicalize, cause: err}
	}
	id, known := entry.volume.FileID(info)
	if !known {
		return FileID{}, ErrCannotCanonicalize
	}
	return id, nil
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
