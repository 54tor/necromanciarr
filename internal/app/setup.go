package app

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"necromanciarr/internal/config"
	"necromanciarr/internal/doctor"
)

// Types de service que l'assistant de premier démarrage sait tester.
const (
	candidateKindRadarr      = "radarr"
	candidateKindSonarr      = "sonarr"
	candidateKindQbittorrent = "qbittorrent"
)

// SetupCandidate est une valeur saisie dans l'assistant, testée avant d'être enregistrée.
type SetupCandidate struct {
	Kind     string `json:"kind"`
	URL      string `json:"url"`
	APIKey   string `json:"apiKey"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// SetupTestResult est le verdict d'un test ; Detail et Hint sont rendus dans la langue du service.
type SetupTestResult struct {
	Status doctor.Status `json:"status"`
	Code   string        `json:"code"`
	Detail string        `json:"detail"`
	Hint   string        `json:"hint,omitempty"`
}

func (svc *service) TestCandidate(ctx context.Context, candidate SetupCandidate) (SetupTestResult, error) {
	if err := validateCandidate(candidate); err != nil {
		return SetupTestResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, doctor.DefaultTimeout)
	defer cancel()
	deps := svc.deps.DoctorDeps
	var outcome doctor.Result
	if candidate.Kind == candidateKindQbittorrent {
		client := config.TorrentClient{Type: candidateKindQbittorrent, BaseURL: candidate.URL, User: candidate.Username, Password: candidate.Password}
		outcome = doctor.CheckTorrentClient(ctx, client, deps)
	} else {
		service := config.ArrService{Kind: candidate.Kind, BaseURL: candidate.URL, APIKey: candidate.APIKey, User: candidate.Username, Password: candidate.Password}
		outcome = doctor.CheckArrService(ctx, service, deps)
	}
	return svc.renderCandidateResult(outcome, candidate), nil
}

func (svc *service) renderCandidateResult(outcome doctor.Result, candidate SetupCandidate) SetupTestResult {
	translator := svc.deps.Translator
	secrets := []string{candidate.APIKey, candidate.Password}
	return SetupTestResult{
		Status: outcome.Status,
		Code:   outcome.Code,
		Detail: config.RedactText(translator.Render(outcome.Detail), secrets...),
		Hint:   config.RedactText(translator.Render(outcome.Hint), secrets...),
	}
}

func validateCandidate(candidate SetupCandidate) error {
	switch candidate.Kind {
	case candidateKindRadarr, candidateKindSonarr:
		if candidate.APIKey == "" {
			return fmt.Errorf("%w: %s: an API key is required", config.ErrUsage, candidate.Kind)
		}
	case candidateKindQbittorrent:
	default:
		return fmt.Errorf("%w: unknown kind %q (valid: radarr, sonarr, qbittorrent)", config.ErrUsage, candidate.Kind)
	}
	parsedURL, err := url.Parse(candidate.URL)
	isHTTP := err == nil && (parsedURL.Scheme == "http" || parsedURL.Scheme == "https") && parsedURL.Host != ""
	if !isHTTP || strings.Contains(candidate.URL, "#") {
		return fmt.Errorf("%w: %s: invalid url (expected http(s)://host[:port])", config.ErrUsage, candidate.Kind)
	}
	return nil
}

const linkDirPermissions = 0o750

// ApplyFix crée un dossier de liens absent, et seulement si la cible est un dossier de liens configuré hors de toute
// racine de bibliothèque.
func (svc *service) ApplyFix(_ context.Context, fixID string, target string) error {
	if fixID != doctor.FixCreateLinkDir {
		return fmt.Errorf("%w: unknown fix %q (valid: %s)", config.ErrUsage, fixID, doctor.FixCreateLinkDir)
	}
	if !slices.ContainsFunc(svc.deps.Config.LinkDirs, func(linkDir string) bool { return sameCleanPath(linkDir, target) }) {
		return fmt.Errorf("%w: the target is not a configured link directory", config.ErrUsage)
	}
	if svc.isUnderLibraryRoot(target) {
		return fmt.Errorf("%w: the target is inside a library root", config.ErrUsage)
	}
	return os.MkdirAll(target, linkDirPermissions)
}

func sameCleanPath(first, second string) bool {
	return filepath.IsAbs(first) && filepath.Clean(first) == filepath.Clean(second)
}

func (svc *service) isUnderLibraryRoot(target string) bool {
	resolvedTarget := resolveThroughExistingAncestor(target)
	return slices.ContainsFunc(svc.deps.Config.LibraryRoots, func(root string) bool {
		return config.PathsOverlap(target, root) || config.PathsOverlap(resolvedTarget, resolveThroughExistingAncestor(root))
	})
}

// resolveThroughExistingAncestor suit les liens symboliques du plus proche ancêtre existant, puis rajoute la
// partie absente : c'est là que MkdirAll écrirait.
func resolveThroughExistingAncestor(path string) string {
	cleaned := filepath.Clean(path)
	missingParts := []string{}
	for existing := cleaned; ; existing = filepath.Dir(existing) {
		resolved, err := filepath.EvalSymlinks(existing)
		if err == nil {
			return filepath.Join(append([]string{resolved}, missingParts...)...)
		}
		if filepath.Dir(existing) == existing {
			return cleaned
		}
		missingParts = append([]string{filepath.Base(existing)}, missingParts...)
	}
}
