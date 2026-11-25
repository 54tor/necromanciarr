package local

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// errParentNotDirectory : un parent existe mais est un fichier ordinaire (ce n'est pas une sortie de la racine).
var errParentNotDirectory = errors.New("local: a parent of the path is not a directory")

// confinedPath est une destination validée : son dossier parent ouvert par handle et son nom final, seul nom
// passé aux appels système (aucun chemin texte n'est résolu une seconde fois).
type confinedPath struct {
	parent     *os.Root
	parentPath string
	name       string
	ownsParent bool
}

func (target confinedPath) close() {
	if target.ownsParent {
		_ = target.parent.Close()
	}
}

// splitRelative refuse le chemin vide, absolu, à volume (Windows), à séparateur initial, et tout segment ".." ou ".".
func splitRelative(relative string) ([]string, error) {
	segments := strings.FieldsFunc(relative, isPathSeparator)
	hasDotSegment := slices.ContainsFunc(segments, func(segment string) bool { return segment == ".." || segment == "." })
	if !filepath.IsLocal(relative) || hasDotSegment || len(segments) == 0 {
		return nil, unconfined("relative path escapes the write root")
	}
	return segments, nil
}

// isPathSeparator ne teste que l'ASCII : tronquer une rune en octet ferait d'un « į » (U+012F) un '/'.
func isPathSeparator(character rune) bool {
	return character == '/' || character == rune(os.PathSeparator)
}

// prepare découpe relative et revérifie que la racine est toujours le dossier ouvert, à son chemin canonique.
func (root *writeRoot) prepare(relative string) ([]string, error) {
	segments, err := splitRelative(relative)
	if err != nil {
		return nil, err
	}
	if err := root.checkRootInPlace(); err != nil {
		return nil, err
	}
	return segments, nil
}

// checkRootInPlace refuse une racine déplacée ou remplacée depuis l'ouverture : le contrôle de recouvrement du
// registre visait son chemin, pas le handle.
func (root *writeRoot) checkRootInPlace() error {
	current, err := os.Lstat(root.dir)
	if err != nil || !os.SameFile(current, root.identity) {
		return unconfined("write root moved or replaced")
	}
	return nil
}

// resolve valide relative et ouvre son dossier parent segment par segment.
func (root *writeRoot) resolve(relative string) (confinedPath, error) {
	segments, err := root.prepare(relative)
	if err != nil {
		return confinedPath{}, err
	}
	return root.openParent(segments)
}

func (root *writeRoot) openParent(segments []string) (confinedPath, error) {
	parentSegments := segments[:len(segments)-1]
	parent, owned, err := root.walkDirectories(parentSegments, 0, false)
	if err != nil {
		return confinedPath{}, err
	}
	parentPath := filepath.Join(append([]string{root.dir}, parentSegments...)...)
	return confinedPath{parent: parent, parentPath: parentPath, name: segments[len(segments)-1], ownsParent: owned}, nil
}

// walkDirectories ouvre chaque segment depuis le handle du précédent ; avec create, crée les manquants (perm).
func (root *writeRoot) walkDirectories(segments []string, perm fs.FileMode, create bool) (*os.Root, bool, error) {
	current, owned := root.root, false
	for _, segment := range segments {
		next, err := root.openChildDirectory(current, segment, perm, create)
		if owned {
			_ = current.Close()
		}
		if err != nil {
			return nil, false, err
		}
		current, owned = next, true
	}
	return current, owned, nil
}

// openChildDirectory refuse un segment symbolique, point d'analyse ou non répertoire, puis compare l'identité du
// handle ouvert au Lstat : un segment remplacé entre les deux est refusé. Il refuse aussi un point de montage.
func (root *writeRoot) openChildDirectory(parent *os.Root, name string, perm fs.FileMode, create bool) (*os.Root, error) {
	info, err := root.lstatOrCreate(parent, name, perm, create)
	if err != nil {
		return nil, err
	}
	if info.Mode().IsRegular() {
		return nil, errParentNotDirectory
	}
	if !isPlainDirectory(info) {
		return nil, unconfined("a parent of the path is a symlink or a special entry")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	if err := root.checkOpenedChild(child, info); err != nil {
		_ = child.Close()
		return nil, err
	}
	return child, nil
}

func (root *writeRoot) lstatOrCreate(parent *os.Root, name string, perm fs.FileMode, create bool) (fs.FileInfo, error) {
	info, err := parent.Lstat(name)
	if !create || !errors.Is(err, fs.ErrNotExist) {
		return info, err
	}
	if mkdirErr := parent.Mkdir(name, perm); mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) {
		return nil, mkdirErr
	}
	return parent.Lstat(name)
}

func (root *writeRoot) checkOpenedChild(child *os.Root, lstatInfo fs.FileInfo) error {
	openedInfo, err := child.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(lstatInfo, openedInfo) {
		return unconfined("a parent of the path changed while opening")
	}
	isMountBoundary, err := crossesMount(root.identity, openedInfo, child)
	if err != nil {
		return err
	}
	if isMountBoundary {
		return unconfined("a parent of the path is a mount point")
	}
	return nil
}
