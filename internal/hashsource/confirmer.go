package hashsource

import (
	"context"
	"errors"
	"strings"

	"necromanciarr/internal/store"
)

// Confirmer confirme un candidat de réparation par l'empreinte du stockage, sans lire son contenu ; il satisfait
// repair.HashConfirmer (le paquet repair importe dedupe, qui importe celui-ci : pas d'import inverse).
type Confirmer struct {
	source HashSource
}

// NewConfirmer s'appuie sur une source de stockage distant (NewRclone) : une source qui lit le fichier serait refusée
// par le contrôle d'origine, mais aurait déjà lu.
func NewConfirmer(source HashSource) *Confirmer {
	return &Confirmer{source: source}
}

// Confirm compare l'empreinte enregistrée avec le lien à celle du candidat, du même algorithme. known est faux quand
// le lien n'a pas d'empreinte, que le candidat est hors montage ou que le stockage n'en fournit pas.
func (confirmer *Confirmer) Confirm(ctx context.Context, link store.Link, candidatePath string) (matches, known bool, err error) {
	if link.HashAlgo == "" || link.Hash == "" {
		return false, false, nil
	}
	result, err := confirmer.source.Hash(ctx, candidatePath, Algorithm(link.HashAlgo))
	if errors.Is(err, ErrNoMountForPath) || errors.Is(err, ErrUnsafePath) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !result.Available || result.Origin != Backend {
		return false, false, nil
	}
	return strings.EqualFold(result.Value, link.Hash), true, nil
}
