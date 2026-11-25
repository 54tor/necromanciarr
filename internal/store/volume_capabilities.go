package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"necromanciarr/internal/fsys"
)

// ErrVolumeCapabilityNotACode refuse une raison ou un nom d'empreinte qui n'a pas la forme d'un code : la ligne ne
// porte jamais d'hôte, de chemin ni de secret.
var ErrVolumeCapabilityNotACode = errors.New("store: volume capabilities hold codes only")

// capabilityCode : forme d'une clé ou d'un code de Capabilities (« atomicReplace », « probe_failed », « sha1 »).
var capabilityCode = regexp.MustCompile(`^[a-z][a-zA-Z0-9_]{0,63}$`)

// storedVolumeCapabilities est la forme JSON de capabilities_json : booléens, codes de raison, noms d'empreinte.
type storedVolumeCapabilities struct {
	FileIdentity   bool              `json:"fileIdentity"`
	VolumeIdentity bool              `json:"volumeIdentity"`
	Symlink        bool              `json:"symlink"`
	Hardlink       bool              `json:"hardlink"`
	AtomicReplace  bool              `json:"atomicReplace"`
	ServerCopy     bool              `json:"serverCopy"`
	Reflink        bool              `json:"reflink"`
	RemoteStat     bool              `json:"remoteStat"`
	RemoteHash     []string          `json:"remoteHash,omitempty"`
	Reasons        map[string]string `json:"reasons,omitempty"`
}

// SaveVolumeCapabilities remplace le dernier résultat de sonde connu de volume. Ce n'est qu'un indice (affichage,
// diagnostic) : toute décision de sécurité relit les capacités sondées sur la session courante du volume.
func (openedStore *Store) SaveVolumeCapabilities(ctx context.Context, volume string, capabilities fsys.Capabilities, at time.Time) error {
	stored := storedVolumeCapabilities(capabilities)
	if err := checkCodes(stored); err != nil {
		return err
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return fmt.Errorf("save volume capabilities: %w", err)
	}
	_, err = openedStore.database.ExecContext(ctx, `
INSERT INTO volume_capabilities (volume, capabilities_json, probed_at) VALUES (?, ?, ?)
ON CONFLICT(volume) DO UPDATE SET capabilities_json = excluded.capabilities_json, probed_at = excluded.probed_at`,
		volume, string(encoded), at.UnixNano())
	if err != nil {
		return fmt.Errorf("save volume capabilities: %w", err)
	}
	return nil
}

// VolumeCapabilities rend le dernier résultat de sonde enregistré pour volume et sa date ; found est faux si le
// volume n'a jamais été sondé.
func (openedStore *Store) VolumeCapabilities(ctx context.Context, volume string) (fsys.Capabilities, time.Time, bool, error) {
	var encoded string
	var probedAtNanoseconds int64
	err := openedStore.database.QueryRowContext(ctx,
		`SELECT capabilities_json, probed_at FROM volume_capabilities WHERE volume = ?`, volume).Scan(&encoded, &probedAtNanoseconds)
	if errors.Is(err, sql.ErrNoRows) {
		return fsys.Capabilities{}, time.Time{}, false, nil
	}
	if err != nil {
		return fsys.Capabilities{}, time.Time{}, false, fmt.Errorf("load volume capabilities: %w", err)
	}
	var stored storedVolumeCapabilities
	if err := json.Unmarshal([]byte(encoded), &stored); err != nil {
		return fsys.Capabilities{}, time.Time{}, false, fmt.Errorf("load volume capabilities: %w", err)
	}
	return fsys.Capabilities(stored), time.Unix(0, probedAtNanoseconds).UTC(), true, nil
}

func checkCodes(stored storedVolumeCapabilities) error {
	for key, code := range stored.Reasons {
		if !capabilityCode.MatchString(key) || !capabilityCode.MatchString(code) {
			return ErrVolumeCapabilityNotACode
		}
	}
	for _, algorithm := range stored.RemoteHash {
		if !capabilityCode.MatchString(algorithm) {
			return ErrVolumeCapabilityNotACode
		}
	}
	return nil
}
