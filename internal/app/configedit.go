package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	"necromanciarr/internal/config"
	"necromanciarr/internal/store"
)

const (
	originEnvironment = "env"
	originFlag        = "flag"
	originDefault     = "default"
)

// ConfigStore est la part du store qui garde la configuration éditable ; *store.Store convient.
type ConfigStore interface {
	config.StoredReader
	SetSetting(ctx context.Context, key string, value []byte) error
	DeleteSetting(ctx context.Context, key string) error
	PutConfigOverride(ctx context.Context, override store.ConfigOverride) error
	DeleteConfigOverride(ctx context.Context, list, itemID string) error
}

// StoredConfigLoader reconstruit la configuration avec le vrai chargement (mêmes options et environnement que le
// processus) sur le contenu de base que rend reader.
type StoredConfigLoader func(reader config.StoredReader) (config.Config, error)

// ConfigView est la configuration éditable, sans aucune valeur secrète ; RestartRequired dit qu'une écriture attend
// le prochain lancement.
type ConfigView struct {
	Settings        map[string]ConfigValue  `json:"settings"`
	Lists           map[string][]ConfigItem `json:"lists"`
	Orphans         []config.OrphanOverride `json:"orphans"`
	RestartRequired bool                    `json:"restartRequired"`
}

// ConfigValue est un réglage effectif : Locked quand une variable d'environnement ou une option l'impose, Value nul
// pour un secret (Set dit qu'il existe).
type ConfigValue struct {
	Value  any    `json:"value"`
	Origin string `json:"origin"`
	Locked bool   `json:"locked"`
	Secret bool   `json:"secret"`
	Set    bool   `json:"set"`
}

// ConfigItem est un élément de liste résolu ; un champ secret présent y vaut null.
type ConfigItem struct {
	ItemID  string         `json:"itemId"`
	Element map[string]any `json:"element"`
	Origin  string         `json:"origin"`
	Drift   bool           `json:"drift"`
}

// storedSnapshot est un contenu de base en mémoire, lu par le chargement pour valider une écriture avant de la faire.
type storedSnapshot struct {
	settings  map[string][]byte
	overrides []store.ConfigOverride
	// removesRow dit que le changement ne fait que retirer une ligne enregistrée.
	removesRow bool
}

func (snapshot storedSnapshot) Settings(context.Context) (map[string][]byte, error) {
	return snapshot.settings, nil
}

func (snapshot storedSnapshot) ConfigOverrides(context.Context) ([]store.ConfigOverride, error) {
	return snapshot.overrides, nil
}

func (svc *service) configEditing() (ConfigStore, StoredConfigLoader, error) {
	if svc.deps.ConfigStore == nil || svc.deps.LoadStoredConfig == nil {
		return nil, nil, fmt.Errorf("%w: configuration store", ErrNotConfigured)
	}
	return svc.deps.ConfigStore, svc.deps.LoadStoredConfig, nil
}

func (svc *service) ConfigView(ctx context.Context) (ConfigView, error) {
	editStore, load, err := svc.configEditing()
	if err != nil {
		return ConfigView{}, err
	}
	loaded, err := svc.loadForView(editStore, load)
	if err != nil {
		return ConfigView{}, err
	}
	overrides, err := editStore.ConfigOverrides(ctx)
	if err != nil {
		return ConfigView{}, err
	}
	settings, err := settingViews(loaded)
	if err != nil {
		return ConfigView{}, err
	}
	return ConfigView{
		Settings: settings, Lists: listViews(loaded.UpstreamElements, overrides),
		Orphans: loaded.OrphanOverrides, RestartRequired: svc.restartRequired.Load(),
	}, nil
}

// loadForView relit la base ; en réparation, une base invalide laisse voir la configuration chargée sans elle.
func (svc *service) loadForView(editStore ConfigStore, load StoredConfigLoader) (config.Config, error) {
	loaded, err := load(editStore)
	isRepairing := svc.deps.Config.StoredLayerSkipped && errors.Is(err, config.ErrInvalidStoredConfig)
	if isRepairing {
		return svc.deps.Config, nil
	}
	return loaded, err
}

func settingViews(loaded config.Config) (map[string]ConfigValue, error) {
	effective, err := effectiveSettings(loaded)
	if err != nil {
		return nil, err
	}
	views := map[string]ConfigValue{}
	for _, key := range config.SettingKeys() {
		origin := settingOrigin(loaded.Origins, key)
		isSecret := config.IsSecretSetting(key)
		view := ConfigValue{
			Origin: origin, Secret: isSecret, Set: origin != originDefault,
			Locked: origin == originEnvironment || origin == originFlag,
		}
		if !isSecret {
			view.Value = effective[key]
		}
		views[key] = view
	}
	return views, nil
}

// effectiveSettings relit la configuration effective par ses noms JSON, qui sont ceux des réglages.
func effectiveSettings(loaded config.Config) (map[string]any, error) {
	encoded, err := json.Marshal(loaded)
	if err != nil {
		return nil, fmt.Errorf("app: config view: %w", err)
	}
	var effective map[string]any
	if err := json.Unmarshal(encoded, &effective); err != nil {
		return nil, fmt.Errorf("app: config view: %w", err)
	}
	return effective, nil
}

func settingOrigin(origins []config.ValueOrigin, key string) string {
	for _, origin := range origins {
		if origin.Key == key && origin.ItemID == "" {
			return origin.Origin
		}
	}
	return originDefault
}

func listViews(upstream map[string][]config.Element, overrides []store.ConfigOverride) map[string][]ConfigItem {
	views := map[string][]ConfigItem{}
	for _, list := range config.ListNames() {
		resolved, _ := config.ResolveList(list, upstream[list], overrides)
		items := make([]ConfigItem, 0, len(resolved))
		for _, element := range resolved {
			items = append(items, ConfigItem{
				ItemID: element.ItemID, Element: maskedElement(list, element.Element),
				Origin: string(element.Origin), Drift: element.Drift,
			})
		}
		views[list] = items
	}
	return views
}

// maskedElement garde la existence d'un champ secret (null) sans jamais sa valeur, scellée ou non.
func maskedElement(list string, element config.Element) map[string]any {
	masked := maps.Clone(map[string]any(element))
	for field := range masked {
		if slices.Contains(config.SecretFields(list), field) {
			masked[field] = nil
		}
	}
	return masked
}
