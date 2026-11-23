//go:build !linux

package linker

// reflink n'est implémenté que sous Linux (FICLONE) : ailleurs l'appelant passe à la sorte suivante demandée.
func reflink(_, _ string) error {
	return ErrUnsupported
}
