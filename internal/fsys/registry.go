package fsys

import (
	"fmt"
	"maps"
)

// Registry résout une Location vers son volume et garde les racines de bibliothèque canoniques par volume.
// Immuable après NewRegistry, donc sûr entre goroutines sans verrou.
type Registry struct {
	volumes      map[string]Volume
	libraryRoots map[string][]string
}

// NewRegistry déclare les volumes (clé : nom du volume, "" pour le disque local) et les racines de bibliothèque,
// canonicalisées une fois par leur volume ; une racine qui ne se canonicalise pas fait échouer la construction.
func NewRegistry(volumes map[string]Volume, libraryRoots []Location) (*Registry, error) {
	registry := &Registry{volumes: maps.Clone(volumes), libraryRoots: map[string][]string{}}
	for _, libraryRoot := range libraryRoots {
		volume, err := registry.volume(libraryRoot.Volume)
		if err != nil {
			return nil, err
		}
		canonical, err := canonicalOf(volume, libraryRoot.Path)
		if err != nil {
			return nil, err
		}
		registry.libraryRoots[libraryRoot.Volume] = append(registry.libraryRoots[libraryRoot.Volume], canonical)
	}
	return registry, nil
}

// Read rend une vue scellée en lecture seule du volume de location (jamais le Volume lui-même, même par
// assertion de type) et le chemin à lui passer (location.Path).
func (registry *Registry) Read(location Location) (ReadFS, string, error) {
	volume, err := registry.volume(location.Volume)
	if err != nil {
		return nil, "", err
	}
	return readOnlyView{inner: volume}, location.Path, nil
}

// WriteRoot est le seul constructeur de WriteRoot hors des paquets de volume : il canonicalise dir par son volume
// et refuse (ErrWriteInLibrary) un dossier égal à une racine de bibliothèque du même volume, qui en contient une
// ou qui y est contenu. Le volume reçoit la forme canonique.
func (registry *Registry) WriteRoot(dir Location) (WriteRoot, error) {
	volume, err := registry.volume(dir.Volume)
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalOf(volume, dir.Path)
	if err != nil {
		return nil, err
	}
	syntax := volume.Paths()
	for _, libraryRoot := range registry.libraryRoots[dir.Volume] {
		overlaps := isSameOrInside(syntax, canonical, libraryRoot) || isSameOrInside(syntax, libraryRoot, canonical)
		if overlaps {
			return nil, fmt.Errorf("%w (volume %q)", ErrWriteInLibrary, dir.Volume)
		}
	}
	return volume.OpenWriteRoot(canonical)
}

func (registry *Registry) volume(name string) (Volume, error) {
	volume, known := registry.volumes[name]
	if !known || volume == nil {
		return nil, fmt.Errorf("%w %q", ErrUnknownVolume, name)
	}
	return volume, nil
}

// canonicalOf exige du volume une forme non relative et propre : une sortie bancale (barre finale, "..", relative)
// fausserait la comparaison par ancêtres.
func canonicalOf(volume Volume, name string) (string, error) {
	canonical, err := volume.Canonical(name)
	if err != nil {
		return "", &canonicalError{cause: err}
	}
	syntax := volume.Paths()
	isCanonical := syntax.IsAbs(canonical) && syntax.Clean(canonical) == canonical
	if !isCanonical {
		return "", ErrCannotCanonicalize
	}
	return canonical, nil
}

// isSameOrInside remonte candidate par Dir jusqu'à la racine du volume, sans comparer de préfixe textuel
// ("/srv/medi" n'est pas dans "/srv/media").
func isSameOrInside(syntax PathSyntax, candidate, ancestor string) bool {
	current := candidate
	for {
		if current == ancestor {
			return true
		}
		parent := syntax.Dir(current)
		if parent == current {
			return false
		}
		current = parent
	}
}

// canonicalError garde la cause pour errors.Is sans recopier son texte, qui peut contenir un chemin.
type canonicalError struct{ cause error }

func (canonicalErr *canonicalError) Error() string { return ErrCannotCanonicalize.Error() }

func (canonicalErr *canonicalError) Unwrap() []error {
	return []error{ErrCannotCanonicalize, canonicalErr.cause}
}
