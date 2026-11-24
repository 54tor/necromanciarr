package crossseed

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"necromanciarr/internal/i18n"
)

// Behavior est ce que le pipeline fait de cross-seed pour un seed.
type Behavior int

const (
	// Delegate confie la recherche de correspondances au démon cross-seed (notification après seed).
	Delegate Behavior = iota
	// Native laisse Necromanciarr chercher lui-même les correspondances, sans cross-seed.
	Native
	// Disabled : un seul torrent par fichier, aucune recherche de correspondance.
	Disabled
)

// Modes possibles du réglage crossSeedMode.
const (
	ModeAuto     = "auto"
	ModeDelegate = "delegate"
	ModeNative   = "native"
	ModeOff      = "off"

	defaultReachTimeout = 2 * time.Second
)

// AllowedModes liste les valeurs acceptées de crossSeedMode.
var AllowedModes = []string{ModeAuto, ModeDelegate, ModeNative, ModeOff}

// NormalizeMode met un mode en minuscules sans espaces ; la chaîne vide vaut auto. Un mode inconnu est une erreur
// qui liste les valeurs acceptées et ne reprend que les 20 premiers caractères fautifs.
func NormalizeMode(mode string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	if normalized == "" {
		return ModeAuto, nil
	}
	for _, allowed := range AllowedModes {
		if normalized == allowed {
			return normalized, nil
		}
	}
	return "", fmt.Errorf("crossSeedMode: unknown value %q (allowed: %s)", truncate(mode, 20), strings.Join(AllowedModes, ", "))
}

func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// Avertissements de ResolveMode : des messages sans paramètre, rendus par l'appelant.
var (
	warningDelegateNoDaemon    = i18n.Message{Key: i18n.KeyWarningCrossSeedDelegateNoDaemon}
	warningDelegateUnreachable = i18n.Message{Key: i18n.KeyWarningCrossSeedDelegateUnreachable}
	warningFallbackNative      = i18n.Message{Key: i18n.KeyWarningCrossSeedFallbackNative}
)

// ResolveMode décide du comportement. L'avertissement, s'il a une clé, est à consigner : delegate sans démon joignable
// n'est jamais silencieusement remplacé par native.
func ResolveMode(mode string, daemonConfigured, daemonReachable bool) (Behavior, i18n.Message, error) {
	normalized, err := NormalizeMode(mode)
	if err != nil {
		return Disabled, i18n.Message{}, err
	}
	usable := daemonConfigured && daemonReachable
	switch normalized {
	case ModeNative:
		return Native, i18n.Message{}, nil
	case ModeOff:
		return Disabled, i18n.Message{}, nil
	case ModeDelegate:
		if usable {
			return Delegate, i18n.Message{}, nil
		}
		if daemonConfigured {
			return Disabled, warningDelegateUnreachable, nil
		}
		return Disabled, warningDelegateNoDaemon, nil
	}
	if usable {
		return Delegate, i18n.Message{}, nil
	}
	if daemonConfigured {
		return Native, warningFallbackNative, nil
	}
	return Native, i18n.Message{}, nil
}

type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Reachable ouvre une connexion TCP vers l'hôte:port de baseURL (80 ou 443 selon le schéma, 2 s par défaut si
// timeout <= 0) puis la ferme : cross-seed n'a pas de point de santé documenté et aucun octet n'est envoyé.
func Reachable(ctx context.Context, baseURL string, timeout time.Duration) bool {
	dialer := net.Dialer{}
	return reachableWith(ctx, baseURL, timeout, dialer.DialContext)
}

func reachableWith(ctx context.Context, baseURL string, timeout time.Duration, dial dialFunc) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	port := parsed.Port()
	if port == "" {
		port = "80"
		if parsed.Scheme == "https" {
			port = "443"
		}
	}
	if timeout <= 0 {
		timeout = defaultReachTimeout
	}
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	connection, err := dial(dialCtx, "tcp", net.JoinHostPort(parsed.Hostname(), port))
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}
