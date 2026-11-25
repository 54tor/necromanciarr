//go:build !linux

package local

import "os"

// cloneFile : le reflink n'existe que sous Linux ; ailleurs CopyFrom copie par flux.
func cloneFile(_, _ *os.File) error {
	return ErrUnsupported
}
