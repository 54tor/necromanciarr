package qbit

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// apiVersion est la version de l'API Web (pas celle de qBittorrent) : 2.11.0 arrive avec qBittorrent 5.0.
type apiVersion struct{ major, minor, patch int }

var (
	// stopStartVersion : qBittorrent 5 renomme pause/resume en stop/start et paused en stopped.
	stopStartVersion = apiVersion{2, 11, 0}
	// exportVersion : torrents/export arrive avec qBittorrent 4.5.
	exportVersion = apiVersion{2, 8, 11}
)

func (version apiVersion) atLeast(other apiVersion) bool {
	if version.major != other.major {
		return version.major > other.major
	}
	if version.minor != other.minor {
		return version.minor > other.minor
	}
	return version.patch >= other.patch
}

func parseAPIVersion(raw string) (apiVersion, error) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(raw), "v"), ".")
	if len(parts) < 2 || len(parts) > 3 {
		return apiVersion{}, fmt.Errorf("%w: unparsable web api version", ErrBadResponse)
	}
	numbers := [3]int{}
	for partIndex, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return apiVersion{}, fmt.Errorf("%w: unparsable web api version", ErrBadResponse)
		}
		numbers[partIndex] = number
	}
	return apiVersion{numbers[0], numbers[1], numbers[2]}, nil
}

// apiVersion sonde la version une seule fois ; un échec n'est pas mis en cache.
func (client *Client) apiVersion(ctx context.Context) (apiVersion, error) {
	client.versionMutex.Lock()
	defer client.versionMutex.Unlock()
	if client.version != nil {
		return *client.version, nil
	}
	payload, err := client.call(ctx, http.MethodGet, endpointAPIVersion, nil, nil, maxSmallBytes)
	if err != nil {
		return apiVersion{}, err
	}
	version, err := parseAPIVersion(string(payload))
	if err != nil {
		return apiVersion{}, err
	}
	client.version = &version
	return version, nil
}

func (client *Client) usesStopStart(ctx context.Context) (bool, error) {
	version, err := client.apiVersion(ctx)
	if err != nil {
		return false, err
	}
	return version.atLeast(stopStartVersion), nil
}
