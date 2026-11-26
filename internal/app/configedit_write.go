package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"necromanciarr/internal/config"
	"necromanciarr/internal/store"
)

const variableReferenceMark = "${"

// ErrUnknownConfigKey refuse une clé de réglage ou une liste que la configuration ne connaît pas.
var ErrUnknownConfigKey = fmt.Errorf("%w: unknown configuration key", config.ErrUsage)

// configWrite enregistre en base une écriture déjà validée ; nil : rien à écrire.
type configWrite func(ctx context.Context, editStore ConfigStore) error

func (svc *service) SetSetting(ctx context.Context, key string, value json.RawMessage) error {
	if err := requireKnownSetting(key); err != nil {
		return err
	}
	storedValue, err := svc.storedSettingValue(key, value)
	if err != nil {
		return err
	}
	return svc.commitConfig(ctx, func(snapshot *storedSnapshot) (configWrite, error) {
		snapshot.settings[key] = storedValue
		return func(ctx context.Context, editStore ConfigStore) error {
			return editStore.SetSetting(ctx, key, storedValue)
		}, nil
	})
}

func (svc *service) UnsetSetting(ctx context.Context, key string) error {
	if err := requireKnownSetting(key); err != nil {
		return err
	}
	return svc.commitConfig(ctx, func(snapshot *storedSnapshot) (configWrite, error) {
		if _, isStored := snapshot.settings[key]; !isStored {
			return nil, nil
		}
		delete(snapshot.settings, key)
		return func(ctx context.Context, editStore ConfigStore) error {
			return editStore.DeleteSetting(ctx, key)
		}, nil
	})
}

func (svc *service) PutItem(ctx context.Context, list, itemID string, element map[string]any) error {
	if err := requireKnownList(list); err != nil {
		return err
	}
	if err := rejectElementReferences(list, element); err != nil {
		return err
	}
	return svc.commitConfig(ctx, func(snapshot *storedSnapshot) (configWrite, error) {
		override, err := svc.itemOverride(*snapshot, list, itemID, element)
		if err != nil {
			return nil, err
		}
		snapshot.putOverride(override)
		return func(ctx context.Context, editStore ConfigStore) error {
			return editStore.PutConfigOverride(ctx, override)
		}, nil
	})
}

func (svc *service) DeleteItem(ctx context.Context, list, itemID string) error {
	if err := requireKnownList(list); err != nil {
		return err
	}
	return svc.commitConfig(ctx, func(snapshot *storedSnapshot) (configWrite, error) {
		upstream, isUpstream := svc.upstreamElement(list, itemID)
		if isUpstream {
			marker := store.ConfigOverride{List: list, ItemID: itemID, Deleted: true, UpstreamFingerprint: config.Fingerprint(upstream)}
			snapshot.putOverride(marker)
			return func(ctx context.Context, editStore ConfigStore) error {
				return editStore.PutConfigOverride(ctx, marker)
			}, nil
		}
		if _, isStored := snapshot.override(list, itemID); !isStored {
			return nil, fmt.Errorf("%w: %s item %q not found", config.ErrUsage, list, itemID)
		}
		return snapshot.removeOverride(list, itemID), nil
	})
}

// UnsetItem retire la surcharge (retour à cross-seed) ; sur un élément ajouté, c'est une suppression ; sans surcharge,
// rien n'est écrit. Une ligne d'une liste inconnue (orpheline) se retire aussi.
func (svc *service) UnsetItem(ctx context.Context, list, itemID string) error {
	return svc.commitConfig(ctx, func(snapshot *storedSnapshot) (configWrite, error) {
		if _, isStored := snapshot.override(list, itemID); !isStored {
			return nil, requireKnownList(list)
		}
		return snapshot.removeOverride(list, itemID), nil
	})
}

// commitConfig applique le changement à une copie de la base, la fait relire par le vrai chargement, puis seulement
// l'écrit : un refus n'écrit rien.
func (svc *service) commitConfig(ctx context.Context, change func(snapshot *storedSnapshot) (configWrite, error)) error {
	editStore, load, err := svc.configEditing()
	if err != nil {
		return err
	}
	svc.configEdits.Lock()
	defer svc.configEdits.Unlock()
	snapshot, err := readSnapshot(ctx, editStore)
	if err != nil {
		return err
	}
	write, err := change(&snapshot)
	if err != nil || write == nil {
		return err
	}
	if _, err := load(snapshot); err != nil {
		return refusedCandidate(err)
	}
	if err := write(ctx, editStore); err != nil {
		return err
	}
	svc.restartRequired.Store(true)
	return nil
}

// refusedCandidate fait du refus d'une candidate une erreur d'usage, distincte d'une base illisible.
func refusedCandidate(err error) error {
	if errors.Is(err, config.ErrInvalidStoredConfig) {
		reason := strings.TrimPrefix(err.Error(), config.ErrInvalidStoredConfig.Error()+": ")
		return fmt.Errorf("%w: configuration refused: %s", config.ErrUsage, reason)
	}
	if errors.Is(err, config.ErrUsage) {
		return err
	}
	return fmt.Errorf("%w: configuration refused: %w", config.ErrUsage, err)
}

func readSnapshot(ctx context.Context, editStore ConfigStore) (storedSnapshot, error) {
	settings, err := editStore.Settings(ctx)
	if err != nil {
		return storedSnapshot{}, err
	}
	overrides, err := editStore.ConfigOverrides(ctx)
	if err != nil {
		return storedSnapshot{}, err
	}
	return storedSnapshot{settings: maps.Clone(settings), overrides: slices.Clone(overrides)}, nil
}

func (snapshot storedSnapshot) override(list, itemID string) (store.ConfigOverride, bool) {
	position := snapshot.overridePosition(list, itemID)
	if position < 0 {
		return store.ConfigOverride{}, false
	}
	return snapshot.overrides[position], true
}

func (snapshot storedSnapshot) overridePosition(list, itemID string) int {
	return slices.IndexFunc(snapshot.overrides, func(override store.ConfigOverride) bool {
		return override.List == list && override.ItemID == itemID
	})
}

// putOverride remplace la surcharge à sa place, ou l'ajoute en fin d'ordre d'insertion, comme le store.
func (snapshot *storedSnapshot) putOverride(override store.ConfigOverride) {
	position := snapshot.overridePosition(override.List, override.ItemID)
	if position < 0 {
		snapshot.overrides = append(snapshot.overrides, override)
		return
	}
	snapshot.overrides[position] = override
}

func (snapshot *storedSnapshot) removeOverride(list, itemID string) configWrite {
	snapshot.overrides = slices.DeleteFunc(snapshot.overrides, func(override store.ConfigOverride) bool {
		return override.List == list && override.ItemID == itemID
	})
	return func(ctx context.Context, editStore ConfigStore) error {
		return editStore.DeleteConfigOverride(ctx, list, itemID)
	}
}

func requireKnownSetting(key string) error {
	if !slices.Contains(config.SettingKeys(), key) {
		return fmt.Errorf("%w: setting %q", ErrUnknownConfigKey, key)
	}
	return nil
}

func requireKnownList(list string) error {
	if !slices.Contains(config.ListNames(), list) {
		return fmt.Errorf("%w: list %q", ErrUnknownConfigKey, list)
	}
	return nil
}

func (svc *service) sealer() (*config.FieldSealer, error) {
	if len(svc.deps.Config.SecretKey) == 0 {
		return nil, nil
	}
	return config.NewSecretSealer(svc.deps.Config.SecretKey)
}

// storedSettingValue valide le JSON et scelle un réglage secret ; les erreurs nomment la clé, jamais la valeur.
func (svc *service) storedSettingValue(key string, value json.RawMessage) ([]byte, error) {
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return nil, fmt.Errorf("%w: setting %q: invalid JSON value", config.ErrUsage, key)
	}
	if err := rejectReferences(key, decoded); err != nil {
		return nil, err
	}
	if !config.IsSecretSetting(key) {
		return value, nil
	}
	plain, isString := decoded.(string)
	if !isString {
		return nil, fmt.Errorf("%w: setting %q must be a string", config.ErrUsage, key)
	}
	sealer, err := svc.sealer()
	if err != nil {
		return nil, err
	}
	sealed, err := sealer.SealSetting(key, plain)
	if err != nil {
		return nil, err
	}
	return json.Marshal(sealed)
}

// rejectElementReferences refuse une référence ${VAR} dans tout champ d'élément : une valeur stockée n'est jamais
// interpolée.
func rejectElementReferences(list string, element map[string]any) error {
	for field, value := range element {
		if err := rejectReferences(list+"."+field, value); err != nil {
			return err
		}
	}
	return nil
}

// rejectReferences refuse une référence ${VAR} ; le message nomme le champ, jamais la variable.
func rejectReferences(field string, value any) error {
	switch typed := value.(type) {
	case string:
		if strings.Contains(typed, variableReferenceMark) {
			return fmt.Errorf("%w: %s: ${VARIABLE} references are not interpolated here, store the value itself", config.ErrUsage, field)
		}
	case []any:
		for _, entry := range typed {
			if err := rejectReferences(field, entry); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, entry := range typed {
			if err := rejectReferences(field+"."+key, entry); err != nil {
				return err
			}
		}
	}
	return nil
}
