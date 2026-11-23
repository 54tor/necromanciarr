package config

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	defaultRcloneTimeout = 60 * time.Second
	mountSeparator       = "="
)

// dedupeVerifyModes sont les valeurs de dedupe.verify ; « auto » est le défaut.
var dedupeVerifyModes = []string{"auto", "full", "sampled", "hash"}

// RcloneMount associe un dossier local (point de montage rclone) à « remote:sousChemin ».
type RcloneMount struct {
	LocalPath string `json:"localPath"`
	Remote    string `json:"remote"`
}

// Rclone règle la lecture des empreintes du stockage distant. RC est l'URL de l'API --rc du processus rclone qui porte
// le montage (identifiants éventuels dans l'URL, masqués à l'affichage) ; Binary le chemin d'un binaire rclone, secours
// pour les installations natives. Sans l'un ni l'autre, aucune empreinte distante n'est demandée.
type Rclone struct {
	RC      string        `json:"rc"`
	Binary  string        `json:"binary"`
	Timeout time.Duration `json:"timeout"`
	Mounts  []RcloneMount `json:"mounts"`
}

// fileRclone est la forme brute de la section : les montages sont des chaînes « cheminLocal=remote:sousChemin ».
type fileRclone struct {
	RC      string   `json:"rc"`
	Binary  string   `json:"binary"`
	Timeout string   `json:"timeout"`
	Mounts  []string `json:"mounts"`
}

func parseRclone(rawRclone fileRclone) (Rclone, error) {
	parsed := Rclone{RC: rawRclone.RC, Binary: rawRclone.Binary, Timeout: defaultRcloneTimeout}
	if rawRclone.RC != "" {
		// Message fixe : l'URL brute peut contenir le mot de passe.
		if _, err := parseHTTPURL(rawRclone.RC); err != nil {
			return Rclone{}, errors.New("rclone.rc must be an http(s) url with a host and no fragment")
		}
	}
	if rawRclone.Timeout != "" {
		timeout, err := time.ParseDuration(rawRclone.Timeout)
		if err != nil || timeout <= 0 {
			return Rclone{}, fmt.Errorf("rclone.timeout: %q is not a positive Go duration", rawRclone.Timeout)
		}
		parsed.Timeout = timeout
	}
	for mountIndex, rawMount := range rawRclone.Mounts {
		mount, err := parseRcloneMount(rawMount)
		if err != nil {
			return Rclone{}, fmt.Errorf("rclone.mounts[%d]: %w", mountIndex, err)
		}
		if slices.ContainsFunc(parsed.Mounts, func(known RcloneMount) bool { return known.LocalPath == mount.LocalPath }) {
			return Rclone{}, fmt.Errorf("rclone.mounts[%d]: duplicate local path %q", mountIndex, mount.LocalPath)
		}
		parsed.Mounts = append(parsed.Mounts, mount)
	}
	return parsed, nil
}

// parseRcloneMount lit « cheminLocal=remote:sousChemin » : un seul « = », chemin local absolu et nettoyé, remote qui
// contient « : » et ne commence pas par « - ».
func parseRcloneMount(rawMount string) (RcloneMount, error) {
	if strings.Count(rawMount, mountSeparator) != 1 {
		return RcloneMount{}, errors.New(`expected "localPath=remote:subPath" with exactly one "="`)
	}
	localPath, remote, _ := strings.Cut(rawMount, mountSeparator)
	hasParentSegment := slices.Contains(strings.Split(filepath.ToSlash(localPath), "/"), "..")
	if !filepath.IsAbs(localPath) || hasParentSegment || strings.ContainsRune(localPath, 0) {
		return RcloneMount{}, fmt.Errorf("local path %q must be absolute without \"..\"", localPath)
	}
	if !strings.Contains(remote, ":") || strings.HasPrefix(remote, "-") || strings.ContainsRune(remote, 0) {
		return RcloneMount{}, fmt.Errorf("remote %q must contain \":\" and not start with \"-\"", remote)
	}
	return RcloneMount{LocalPath: filepath.Clean(localPath), Remote: remote}, nil
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
