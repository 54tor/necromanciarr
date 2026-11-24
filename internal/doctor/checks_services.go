package doctor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"necromanciarr/internal/arr"
	"necromanciarr/internal/config"
	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/qbit"
)

func newArrPinger(service config.ArrService) Pinger {
	return arr.New(service.Kind, service.BaseURL, service.APIKey, arr.WithBasicAuth(service.User, service.Password))
}

// arrChecks crée un contrôle par instance, identifié « arr:<kind>:<rang dans la liste du kind> ».
func arrChecks(settings config.Config, deps Deps) []Check {
	var checks []Check
	for _, services := range [][]config.ArrService{settings.Radarr, settings.Sonarr} {
		for serviceIndex, service := range services {
			checks = append(checks, Check{
				ID:    fmt.Sprintf("arr:%s:%d", service.Kind, serviceIndex),
				Title: fmt.Sprintf("%s instance %d", service.Kind, serviceIndex),
				Run:   func(ctx context.Context) Result { return checkArr(ctx, service, deps) },
			})
		}
	}
	return checks
}

func checkArr(ctx context.Context, service config.ArrService, deps Deps) Result {
	err := deps.NewPinger(service).Ping(ctx)
	switch {
	case err == nil:
		return Result{Status: OK, Detail: "reachable, API key accepted"}
	case errors.Is(err, arr.ErrAccessDenied):
		return Result{Status: Fail, Detail: "reachable, but the API key is rejected", Hint: "check the apikey of this instance in config.json"}
	case errors.Is(err, context.DeadlineExceeded):
		return Result{Status: Fail, Detail: "timed out", Hint: "check the URL and that the instance is running"}
	}
	return Result{Status: Fail, Detail: "unreachable: " + err.Error(), Hint: "check the URL and that the instance is running"}
}

func checkQbittorrent(ctx context.Context, settings config.Config, deps Deps) Result {
	unsupported := settings.CrossSeedImport.UnsupportedTorrentClients
	if len(settings.TorrentClients) == 0 && len(unsupported) == 0 {
		return Result{Status: Skipped, Detail: "no torrent client configured"}
	}
	var collected findings
	for clientIndex, client := range settings.TorrentClients {
		collected.add(inspectTorrentClient(ctx, clientIndex, client, deps))
	}
	for _, ignored := range unsupported {
		collected.add(Warn, fmt.Sprintf("torrentClients[%d]: type %q is not supported", ignored.Index, ignored.Type),
			"only qbittorrent is supported; this client is ignored")
	}
	return collected.result()
}

func inspectTorrentClient(ctx context.Context, clientIndex int, client config.TorrentClient, deps Deps) (Status, string, string) {
	label := fmt.Sprintf("torrentClients[%d]", clientIndex)
	torrentClient, err := deps.NewTorrentClient(client)
	if err != nil {
		return Fail, label + ": " + err.Error(), "check the client URL"
	}
	version, err := torrentClient.Version(ctx)
	if err != nil {
		return Fail, label + ": " + describeQbitError(err), "check the URL, user and password"
	}
	torrents, err := torrentClient.Torrents(ctx)
	if err != nil {
		return Fail, label + ": login ok but listing torrents failed: " + describeQbitError(err), ""
	}
	detail := fmt.Sprintf("%s: API %s, %d torrents", label, version, len(torrents))
	if client.ReadOnly {
		detail += ", read-only (no torrent will be added)"
	}
	return OK, detail, ""
}

func describeQbitError(err error) string {
	if errors.Is(err, qbit.ErrAuthFailed) {
		return "login rejected"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return err.Error()
}

func checkCrossSeed(ctx context.Context, settings config.Config, deps Deps) Result {
	imported := settings.CrossSeedImport
	if !imported.Configured && settings.CrossSeed == nil {
		return Result{Status: Skipped, Detail: "cross-seed is not configured"}
	}
	parts := []string{}
	if imported.Configured {
		parts = append(parts, fmt.Sprintf("config.js imported (keys: %s; %d ignored)",
			strings.Join(imported.ImportedKeys, ", "), len(imported.IgnoredKeys)+imported.UnnamedIgnoredKeys))
	}
	reachable := settings.CrossSeed != nil && deps.Reachable(ctx, settings.CrossSeed.BaseURL)
	parts = append(parts, daemonSummary(settings.CrossSeed != nil, reachable))
	behavior, warning, err := crossseed.ResolveMode(settings.CrossSeedMode, settings.CrossSeed != nil, reachable)
	if err != nil {
		return Result{Status: Fail, Detail: err.Error(), Hint: "allowed: " + strings.Join(crossseed.AllowedModes, ", ")}
	}
	parts = append(parts, fmt.Sprintf("mode %s resolves to %s", settings.CrossSeedMode, behaviorName(behavior)))
	if blocked := imported.BlockedEnvironment; len(blocked) > 0 {
		warning = strings.TrimSpace(warning + " config.js reads variables of this tool, hidden from it: " + strings.Join(blocked, ", "))
	}
	if warning != "" {
		return Result{Status: Warn, Detail: strings.Join(parts, separatorDetail), Hint: warning}
	}
	return Result{Status: OK, Detail: strings.Join(parts, separatorDetail)}
}

func daemonSummary(configured, reachable bool) string {
	switch {
	case !configured:
		return "no daemon configured"
	case reachable:
		return "daemon reachable (TCP)"
	}
	return "daemon unreachable (TCP)"
}

func behaviorName(behavior crossseed.Behavior) string {
	switch behavior {
	case crossseed.Delegate:
		return "delegate to the daemon"
	case crossseed.Native:
		return "native matching"
	}
	return "disabled"
}
