package fsys

import (
	"errors"
	"fmt"
	"runtime"
)

// localCaseInsensitive : NTFS et APFS (par défaut) ignorent la casse ; le registre replie alors sa clé de
// comparaison du volume local. Lu une fois par NewRegistry.
var localCaseInsensitive = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// Registry résout une Location vers son volume et garde, par volume, les racines de bibliothèque. Leur forme
// canonique est calculée paresseusement au premier WriteRoot du volume et gardée seulement après succès : un
// volume injoignable n'empêche ni la construction, ni Read, ni l'usage des autres ; ses WriteRoot échouent
// jusqu'à ce qu'un chargement réussisse. Sûr entre goroutines.
//
// Le contrôle d'I1 compare deux choses : les chemins canoniques, toujours, puis les identités de fichier quand le
// volume les fournit (Capabilities.FileIdentity ou RemoteStat), ce qui attrape les graphies qui échappent au texte
// (casse, NFD, noms 8.3, montages liés). Un chemin dont Stat réussit mais dont l'identité est inconnue (inode nul,
// FUSE) garde le seul plancher textuel, raison notée ; une erreur de Stat refuse toujours. Risque résiduel : sans identité, un alias non résolu par Canonical passe ;
// un alias entre deux volumes (disque local et sftp://localhost sur le même disque) n'est jamais vu (doctor
// avertira). Fenêtre TOCTOU : entre Canonical et OpenWriteRoot, un dossier peut être remplacé ; le volume compare
// l'identité de la racine ouverte au Stat fait juste avant (contrat de OpenWriteRoot). Un alias de la
// bibliothèque SOUS la racine d'écriture (montage lié posé dedans après coup) n'est pas vu ici : le volume
// revérifie l'identité à chaque opération dans sa résolution de chemin (contrat C2), doctor le signalera (C16).
// Ce recontrôle de la racine (local : Lstat du chemin canonique comparé au handle) laisse lui-même une fenêtre
// entre le contrôle et l'appel système ; sous Linux avant 5.8, un montage lié du même système de fichiers n'est
// pas vu (Capabilities.Reasons["mountRoot"]).
type Registry struct {
	volumes map[string]*volumeEntry
}

// RegistryOption règle un Registry à sa construction.
type RegistryOption func(*Registry) error

// WithWriteRootCheck fait passer chaque racine d'écriture ouverte sur volume par check avant de la rendre : un refus
// la ferme et rend l'erreur de check (sonde des capacités d'un volume distant avant tout lien). Volume non déclaré :
// ErrUnknownVolume.
func WithWriteRootCheck(volume string, check func(WriteRoot) error) RegistryOption {
	return func(registry *Registry) error {
		entry, err := registry.entry(volume)
		if err != nil {
			return err
		}
		entry.writeRootCheck = check
		return nil
	}
}

// NewRegistry déclare les volumes (clé : nom du volume, "" pour le disque local ; chaque clé égale à
// Volume()) et les racines de bibliothèque. Aucun accès disque ou réseau : seules les erreurs de déclaration
// (volume inconnu, nom différent de sa clé) la font échouer.
func NewRegistry(volumes map[string]Volume, libraryRoots []Location, options ...RegistryOption) (*Registry, error) {
	registry := &Registry{volumes: map[string]*volumeEntry{}}
	for name, volume := range volumes {
		if volume == nil || volume.Volume() != name {
			return nil, fmt.Errorf("%w %q: volume name differs from its key", ErrUnknownVolume, name)
		}
		isLocal := name == ""
		registry.volumes[name] = &volumeEntry{volume: volume, foldCase: isLocal && localCaseInsensitive}
	}
	for _, libraryRoot := range libraryRoots {
		entry, err := registry.entry(libraryRoot.Volume)
		if err != nil {
			return nil, err
		}
		entry.rawLibraryRoots = append(entry.rawLibraryRoots, libraryRoot.Path)
	}
	for _, option := range options {
		if err := option(registry); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// stageResetter est un volume qui borne ses reconnexions par étape (sftpfs).
type stageResetter interface{ ResetForStage() }

// ResetForStage ouvre une nouvelle étape sur chaque volume qui le demande : une reconnexion y redevient possible.
func (registry *Registry) ResetForStage() {
	for _, entry := range registry.volumes {
		if resetter, resets := entry.volume.(stageResetter); resets {
			resetter.ResetForStage()
		}
	}
}

// Read rend une vue scellée en lecture seule du volume de location (jamais le Volume lui-même, même par
// assertion de type) et le chemin à lui passer (location.Path).
func (registry *Registry) Read(location Location) (ReadFS, string, error) {
	entry, err := registry.entry(location.Volume)
	if err != nil {
		return nil, "", err
	}
	return readOnlyView{inner: entry.volume}, location.Path, nil
}

// WriteRoot est le seul constructeur de WriteRoot hors des paquets de volume : il refuse (ErrNoLibraryRoots) un
// registre sans aucune racine de bibliothèque (lecture seule), puis il canonicalise dir par son volume
// et refuse (ErrWriteInLibrary) un dossier égal à une racine de bibliothèque du même volume, qui en contient une
// ou qui y est contenu, par le texte canonique et, si le volume la fournit, par l'identité de fichier. Le volume
// reçoit la forme canonique (sur disque, casse d'origine).
func (registry *Registry) WriteRoot(dir Location) (WriteRoot, error) {
	if !registry.hasLibraryRoots() {
		return nil, ErrNoLibraryRoots
	}
	entry, err := registry.entry(dir.Volume)
	if err != nil {
		return nil, err
	}
	state, err := entry.load()
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalOf(entry.volume, dir.Path)
	if err != nil {
		return nil, err
	}
	overlaps, err := entry.overlapsLibrary(state, canonical)
	if err != nil {
		return nil, err
	}
	if overlaps {
		return nil, fmt.Errorf("%w (volume %q)", ErrWriteInLibrary, dir.Volume)
	}
	root, err := entry.volume.OpenWriteRoot(canonical)
	if err != nil || entry.writeRootCheck == nil {
		return root, err
	}
	if err := entry.writeRootCheck(root); err != nil {
		return nil, errors.Join(err, root.Close())
	}
	return root, nil
}

func (registry *Registry) hasLibraryRoots() bool {
	for _, entry := range registry.volumes {
		if len(entry.rawLibraryRoots) > 0 {
			return true
		}
	}
	return false
}

func (registry *Registry) entry(name string) (*volumeEntry, error) {
	entry, known := registry.volumes[name]
	if !known {
		return nil, fmt.Errorf("%w %q", ErrUnknownVolume, name)
	}
	return entry, nil
}

// canonicalOf exige du volume une forme non relative et propre : une sortie bancale (barre finale, "..", relative)
// fausserait la comparaison par ancêtres.
func canonicalOf(volume Volume, name string) (string, error) {
	canonical, err := volume.Canonical(name)
	if err != nil {
		return "", &causeHidingError{sentinel: ErrCannotCanonicalize, cause: err}
	}
	syntax := volume.Paths()
	isCanonical := syntax.IsAbs(canonical) && syntax.Clean(canonical) == canonical
	if !isCanonical {
		return "", ErrCannotCanonicalize
	}
	return rootSpelling(syntax, canonical), nil
}

// rootSpelling donne à une racine de volume l'écriture que Dir rend pour le parent de ses enfants : sous Windows,
// `\\server\share` devient `\\server\share\`, sinon une racine de partage écrite sans séparateur ne serait
// jamais égale à l'ancêtre d'une bibliothèque placée dessous (`C:` seul, relatif au lecteur, est déjà refusé).
func rootSpelling(syntax PathSyntax, canonical string) string {
	isVolumeRoot := syntax.Dir(canonical) == canonical
	if !isVolumeRoot {
		return canonical
	}
	return syntax.Dir(syntax.Join(canonical, "child"))
}

// causeHidingError garde la cause pour errors.Is sans recopier son texte, qui peut contenir un chemin.
type causeHidingError struct{ sentinel, cause error }

func (hidingErr *causeHidingError) Error() string { return hidingErr.sentinel.Error() }

func (hidingErr *causeHidingError) Unwrap() []error {
	return []error{hidingErr.sentinel, hidingErr.cause}
}
