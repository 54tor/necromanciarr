package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	defaultRcloneTimeout = 60 * time.Second
	mountSeparator       = "="
	// FSProviderTypeRclone est le type des fournisseurs qui exposent un stockage distant monté par rclone.
	FSProviderTypeRclone = "rclone"
)

// fsProviderTypes liste les types connus, dans l'ordre affiché par les erreurs.
var fsProviderTypes = []string{FSProviderTypeRclone, FSProviderTypeSFTP, FSProviderTypeLocal}

var fsProviderNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

// dedupeVerifyModes sont les valeurs de dedupe.verify ; « auto » est le défaut.
var dedupeVerifyModes = []string{"auto", "full", "sampled", "hash"}

// FSMount associe un dossier local à la cible du fournisseur ; la forme de Target dépend du type (rclone :
// « remote:sousChemin »).
type FSMount struct {
	LocalPath string `json:"localPath"`
	Target    string `json:"target"`
}

// FSProvider décrit un fournisseur de système de fichiers. Mounts, RC et Binary ne servent qu'au type rclone ;
// Address à Exec qu'au type sftp ; Timeout aux deux ; Paths qu'au type local. RC, Password et IdentityPassphrase sont des secrets, masqués à
// tout affichage ; fsProviders ne vient que de la base, où l'édition de la configuration scelle ces secrets.
type FSProvider struct {
	Type                  string        `json:"type"`
	Name                  string        `json:"name"`
	Mounts                []FSMount     `json:"mounts"`
	Paths                 []string      `json:"paths,omitempty"`
	RC                    string        `json:"rc,omitempty"`
	Binary                string        `json:"binary,omitempty"`
	Timeout               time.Duration `json:"timeout,omitempty"`
	Address               string        `json:"address,omitempty"`
	User                  string        `json:"user,omitempty"`
	IdentityFile          string        `json:"identityFile,omitempty"`
	KnownHosts            string        `json:"knownHosts,omitempty"`
	Agent                 bool          `json:"agent,omitempty"`
	Password              string        `json:"password,omitempty"`
	IdentityPassphrase    string        `json:"identityPassphrase,omitempty"`
	ReadBytesPerSecond    int64         `json:"readBytesPerSecond,omitempty"`
	MaxConcurrentRequests int           `json:"maxConcurrentRequests,omitempty"`
	Keepalive             time.Duration `json:"keepalive,omitempty"`
	ExecHashTimeout       time.Duration `json:"execHashTimeout,omitempty"`
	Exec                  string        `json:"exec,omitempty"`
}

type plainProvider FSProvider

func (provider FSProvider) redacted() plainProvider {
	provider.RC = redactRCURL(provider.RC)
	provider.Password = redactedIfSet(provider.Password)
	provider.IdentityPassphrase = redactedIfSet(provider.IdentityPassphrase)
	return plainProvider(provider)
}

// redactedIfSet remplace un secret présent par la marque ; une valeur absente reste vide, pour dire qu'elle manque.
func redactedIfSet(secret string) string {
	if secret == "" {
		return ""
	}
	return redactedMark
}

// String masque les identifiants de l'URL rc pour %v et %+v.
func (provider FSProvider) String() string { return fmt.Sprintf("%+v", provider.redacted()) }

// GoString masque les identifiants pour %#v.
func (provider FSProvider) GoString() string { return provider.String() }

// LogValue masque les identifiants dans les journaux slog.
func (provider FSProvider) LogValue() slog.Value { return slog.StringValue(provider.String()) }

// MarshalJSON masque les identifiants : toute sérialisation est destinée à l'affichage.
func (provider FSProvider) MarshalJSON() ([]byte, error) { return json.Marshal(provider.redacted()) }

// rcloneProviderFile est la forme brute d'une entrée de type rclone ; les montages sont « cheminLocal=remote:sousChemin ».
type rcloneProviderFile struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Mounts  []string `json:"mounts"`
	RC      string   `json:"rc"`
	Binary  string   `json:"binary"`
	Timeout string   `json:"timeout"`
}

// parseFSProviders décode chaque entrée selon son type (clés inconnues refusées), puis vérifie
// l'unicité des noms et des chemins locaux.
func parseFSProviders(rawProviders []json.RawMessage) ([]FSProvider, error) {
	var providers []FSProvider
	for providerIndex, rawProvider := range rawProviders {
		provider, err := parseFSProvider(rawProvider)
		if err != nil {
			return nil, fmt.Errorf("fsProviders[%d]: %w", providerIndex, err)
		}
		providers = append(providers, provider)
	}
	if err := checkProvidersAreDistinct(providers); err != nil {
		return nil, err
	}
	return providers, nil
}

func parseFSProvider(rawProvider json.RawMessage) (FSProvider, error) {
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(rawProvider, &header); err != nil {
		return FSProvider{}, errors.New("must be a JSON object with a string \"type\"")
	}
	switch header.Type {
	case "":
		return FSProvider{}, fmt.Errorf("type is required (known types: %s)", strings.Join(fsProviderTypes, ", "))
	case FSProviderTypeRclone:
		return parseRcloneProvider(rawProvider)
	case FSProviderTypeSFTP:
		return parseSFTPProvider(rawProvider)
	case FSProviderTypeLocal:
		return parseLocalProvider(rawProvider)
	default:
		return FSProvider{}, fmt.Errorf("unknown type %q (known types: %s)", header.Type, strings.Join(fsProviderTypes, ", "))
	}
}

func parseRcloneProvider(rawProvider json.RawMessage) (FSProvider, error) {
	var rawFile rcloneProviderFile
	decoder := json.NewDecoder(bytes.NewReader(rawProvider))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rawFile); err != nil {
		return FSProvider{}, fmt.Errorf("type rclone: %w", err)
	}
	if !fsProviderNamePattern.MatchString(rawFile.Name) {
		return FSProvider{}, fmt.Errorf("name %q is required and must match %s", rawFile.Name, fsProviderNamePattern)
	}
	provider := FSProvider{Type: rawFile.Type, Name: rawFile.Name, RC: rawFile.RC, Binary: rawFile.Binary, Timeout: defaultRcloneTimeout}
	if rawFile.RC != "" {
		// Message fixe : l'URL brute peut contenir le mot de passe.
		if _, err := parseHTTPURL(rawFile.RC); err != nil {
			return FSProvider{}, errors.New("rc must be an http(s) url with a host and no fragment")
		}
	}
	if rawFile.Timeout != "" {
		timeout, err := time.ParseDuration(rawFile.Timeout)
		if err != nil || timeout <= 0 {
			return FSProvider{}, fmt.Errorf("timeout: %q is not a positive Go duration", rawFile.Timeout)
		}
		provider.Timeout = timeout
	}
	for mountIndex, rawMount := range rawFile.Mounts {
		mount, err := parseRcloneMount(rawMount)
		if err != nil {
			return FSProvider{}, fmt.Errorf("mounts[%d]: %w", mountIndex, err)
		}
		provider.Mounts = append(provider.Mounts, mount)
	}
	return provider, nil
}

// parseRcloneMount lit « cheminLocal=remote:sousChemin » : un seul « = », chemin local absolu et nettoyé, remote qui
// contient « : » et ne commence pas par « - ».
func parseRcloneMount(rawMount string) (FSMount, error) {
	if strings.Count(rawMount, mountSeparator) != 1 {
		return FSMount{}, errors.New(`expected "localPath=remote:subPath" with exactly one "="`)
	}
	localPath, remote, _ := strings.Cut(rawMount, mountSeparator)
	hasParentSegment := slices.Contains(strings.Split(filepath.ToSlash(localPath), "/"), "..")
	if !filepath.IsAbs(localPath) || hasParentSegment || strings.ContainsRune(localPath, 0) {
		return FSMount{}, fmt.Errorf("local path %q must be absolute without \"..\"", localPath)
	}
	if !strings.Contains(remote, ":") || strings.HasPrefix(remote, "-") || strings.ContainsRune(remote, 0) {
		return FSMount{}, fmt.Errorf("remote %q must contain \":\" and not start with \"-\"", remote)
	}
	return FSMount{LocalPath: filepath.Clean(localPath), Target: remote}, nil
}

// checkProvidersAreDistinct refuse un nom repris ou un chemin local déclaré deux fois (montage rclone ou dossier d'un
// volume local). L'imbrication reste autorisée : le plus long préfixe gagne à la recherche.
func checkProvidersAreDistinct(providers []FSProvider) error {
	seenNames := map[string]bool{}
	pathOwners := map[string]string{}
	for _, provider := range providers {
		if seenNames[provider.Name] {
			return fmt.Errorf("fsProviders: duplicate name %q", provider.Name)
		}
		seenNames[provider.Name] = true
		for _, localPath := range ownedPaths(provider) {
			if owner, taken := pathOwners[localPath]; taken {
				return fmt.Errorf("fsProviders: local path %q is mounted by both %q and %q", localPath, owner, provider.Name)
			}
			pathOwners[localPath] = provider.Name
		}
	}
	return nil
}

func parseDedupeVerify(rawVerify string) (string, error) {
	if rawVerify == "" {
		return "auto", nil
	}
	if !slices.Contains(dedupeVerifyModes, rawVerify) {
		return "", fmt.Errorf("dedupe.verify: unknown mode %q (allowed: %v)", rawVerify, dedupeVerifyModes)
	}
	return rawVerify, nil
}

// redactRCURL masque tout le userinfo de l'URL rc : nom d'utilisateur et mot de passe.
func redactRCURL(rawURL string) string {
	if rawURL == "" {
		return rawURL
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return redactedMark
	}
	if parsedURL.User != nil {
		parsedURL.User = url.User(redactedMark)
	}
	return parsedURL.String()
}
