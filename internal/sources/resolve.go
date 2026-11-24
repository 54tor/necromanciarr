package sources

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"

	"necromanciarr/internal/config"
)

// AnnounceSource dit d'où vient l'annonce d'un candidat : config, indexer ou none.
type AnnounceSource string

const (
	// AnnounceConfig : une entrée trackers couvre le tracker du candidat ; elle remplace toute autre annonce.
	AnnounceConfig AnnounceSource = "config"
	// AnnounceIndexer : l'annonce d'un .torrent scellé de même infohash, venu d'un indexeur.
	AnnounceIndexer AnnounceSource = "indexer"
	// AnnounceNone : aucune annonce, le candidat n'est pas seedable (no_announce).
	AnnounceNone AnnounceSource = "none"
)

// Resolver résout l'annonce d'un candidat d'après la section trackers chargée.
type Resolver struct {
	trackers config.Config
}

// Resolution est le résultat de Resolve. Tiers n'est rempli que pour AnnounceConfig et porte la passkey : String,
// GoString, LogValue et MarshalJSON le rendent sous config.RedactAnnounce, comme Tiers lui-même.
type Resolution struct {
	Source     AnnounceSource
	Tiers      Tiers
	TrackerKey string
}

type redactedResolution struct {
	Source     AnnounceSource `json:"source"`
	TrackerKey string         `json:"trackerKey"`
	Tiers      [][]string     `json:"tiers"`
}

// NewResolver garde les entrées trackers ; la comparaison passe par config.Config.TrackerFor, seule règle des trackers.
func NewResolver(trackers []config.Tracker) Resolver {
	return Resolver{trackers: config.Config{Trackers: slices.Clone(trackers)}}
}

// Resolve applique la précédence : la configuration gagne et remplace en silence l'annonce d'un indexeur, sinon le
// .torrent scellé de même infohash, sinon rien. Un tracker vide (après NormalizeTracker) n'a que la voie indexeur.
func (resolver Resolver) Resolve(tracker string, hasSealedIndexerTorrent bool) Resolution {
	if config.NormalizeTracker(tracker) != "" {
		if configured, found := resolver.trackers.TrackerFor(tracker); found {
			return Resolution{Source: AnnounceConfig, Tiers: Tiers{slices.Clone(configured.Announce)}, TrackerKey: configured.Key}
		}
	}
	if hasSealedIndexerTorrent {
		return Resolution{Source: AnnounceIndexer}
	}
	return Resolution{Source: AnnounceNone}
}

func (resolution Resolution) redacted() redactedResolution {
	return redactedResolution{Source: resolution.Source, TrackerKey: resolution.TrackerKey, Tiers: resolution.Tiers.redacted()}
}

// String masque les annonces pour %v, %+v et %s.
func (resolution Resolution) String() string {
	return fmt.Sprintf("sources.Resolution%+v", resolution.redacted())
}

// GoString masque les annonces pour %#v.
func (resolution Resolution) GoString() string { return resolution.String() }

// LogValue masque les annonces dans les journaux slog.
func (resolution Resolution) LogValue() slog.Value { return slog.StringValue(resolution.String()) }

// MarshalJSON rend la source, la clé et l'hôte de chaque annonce, jamais la passkey.
func (resolution Resolution) MarshalJSON() ([]byte, error) {
	return json.Marshal(resolution.redacted())
}
