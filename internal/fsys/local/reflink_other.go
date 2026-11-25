//go:build !linux

package local

// Reflink n'est implémenté que sous Linux (FICLONE) : ailleurs l'appelant passe à la sorte suivante demandée.
func Reflink(_, _ string) error {
	return ErrUnsupported
}
