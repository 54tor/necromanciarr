package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"necromanciarr/internal/i18n"
	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// Raisons de l'annonce, communes à Verify, Link et Seed.
const (
	// ReasonNoAnnounce : ni entrée trackers ni .torrent scellé d'indexeur ; rien n'est lié ni ajouté.
	ReasonNoAnnounce = "no_announce"
	// ReasonSealedTorrentUnreadable : le .torrent scellé ne se déchiffre plus (clé changée) ; relancer Verify.
	ReasonSealedTorrentUnreadable = "sealed_torrent_unreadable"
	// ReasonSecretKeyMissing : un .torrent scellé attend sa clé, absente de cette exécution.
	ReasonSecretKeyMissing = "secret_key_missing"
	// ReasonInfohashChangedAfterInject : le .torrent construit n'a plus l'infohash du candidat ; rien n'est ajouté.
	ReasonInfohashChangedAfterInject = "infohash_changed_after_inject"
)

// AnnounceSourceOf dit d'où viendra l'annonce du candidat, d'après la section trackers courante et le .torrent scellé
// éventuel : jamais stocké, recalculé à chaque lecture (ajouter une entrée trackers suffit, sans revérifier).
func AnnounceSourceOf(resolver sources.Resolver, candidate store.Candidate) sources.AnnounceSource {
	return resolver.Resolve(candidate.Tracker, candidate.HasSealed).Source
}

// WithAnnounce renvoie les détails complétés de seedable et announce_source, calculés à la lecture.
func (details ProposalDetails) WithAnnounce(source sources.AnnounceSource) ProposalDetails {
	details.Seedable = source != sources.AnnounceNone
	details.AnnounceSource = string(source)
	return details
}

// NoAnnounceWarning est l'avertissement d'une proposition sans annonce, torrent privé ou public.
func NoAnnounceWarning(tracker string) i18n.Message {
	return i18n.Message{Key: i18n.KeyWarningNoAnnounce, Params: map[string]string{"tracker": tracker}}
}

// storedProposalDetails écrit les détails sans les champs calculés à la lecture : à profondeur moindre, ces champs
// nil masquent ceux de ProposalDetails (règle de encoding/json), qui n'apparaissent donc jamais en base.
type storedProposalDetails struct {
	ProposalDetails
	Seedable       *bool   `json:"seedable,omitempty"`
	AnnounceSource *string `json:"announce_source,omitempty"`
}

// encodeStoredDetails rend les détails tels qu'ils sont enregistrés (proposition ou verdict mis de côté).
func encodeStoredDetails(details ProposalDetails) ([]byte, error) {
	encoded, err := json.Marshal(storedProposalDetails{ProposalDetails: details})
	if err != nil {
		return nil, fmt.Errorf("encode proposal details: %w", err)
	}
	return encoded, nil
}

// hasInfohash dit si le .torrent a l'infohash attendu, relu par un décodeur indépendant de l'injection.
func hasInfohash(torrent []byte, infohash string) bool {
	parsed, err := torrentcheck.Parse(torrent)
	return err == nil && strings.EqualFold(parsed.InfoHash, infohash)
}

// redactedError porte le texte masqué d'une erreur qui sort de Seed après la lecture d'une annonce d'indexeur ; la
// cause reste accessible à errors.Is et errors.As, jamais à l'affichage.
type redactedError struct {
	text  string
	cause error
}

func (redacted redactedError) Error() string { return redacted.text }

func (redacted redactedError) Unwrap() error { return redacted.cause }

// errIsNoAnnounce dit si l'annonce lue est absente ou inutilisable : le candidat est alors sans annonce.
func errIsNoAnnounce(err error) bool {
	return errors.Is(err, sources.ErrNoAnnounce) || errors.Is(err, sources.ErrInvalidAnnounce)
}
