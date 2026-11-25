// Package local est le volume fsys du disque local, et porte les aides propres à chaque plateforme (type de
// système de fichiers, place libre, reflink, périphérique) partagées avec linker et library.
package local

import "errors"

// Erreurs typées des liens ; leur texte est celui de linker, qui les réexporte sans changement.
var (
	// ErrCrossDevice signale un lien entre deux systèmes de fichiers.
	ErrCrossDevice = errors.New("linker: link across filesystems")
	// ErrLinkRefused signale un lien refusé par le système (droits, nombre maximal de liens, volume sans liens).
	ErrLinkRefused = errors.New("linker: link refused by the system")
	// ErrUnsupported signale une sorte de lien que la plateforme ou le volume ne sait pas faire.
	ErrUnsupported = errors.New("linker: link kind unsupported")
	// ErrDestinationExists signale une destination déjà présente.
	ErrDestinationExists = errors.New("linker: destination already exists")
)
