//go:build !linux && !windows

package local

import "errors"

// FreeBytes n'est pas implémenté ici : sans place libre connue, aucune copie complète n'est tentée.
func FreeBytes(_ string) (uint64, error) {
	return 0, errors.ErrUnsupported
}
