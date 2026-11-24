//go:build !linux && !windows

package linker

import "errors"

// freeBytes n'est pas implémenté ici : sans place libre connue, aucune copie complète n'est tentée.
func freeBytes(_ string) (uint64, error) {
	return 0, errors.ErrUnsupported
}
