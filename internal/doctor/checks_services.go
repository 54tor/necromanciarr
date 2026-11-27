package doctor

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"necromanciarr/internal/arr"
	"necromanciarr/internal/config"
	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/i18n"
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
				ID:          fmt.Sprintf("arr:%s:%d", service.Kind, serviceIndex),
				TitleKey:    i18n.KeyDoctorTitleArr,
				TitleParams: map[string]string{"kind": service.Kind, "index": strconv.Itoa(serviceIndex)},
				Run:         func(ctx context.Context) Result { return CheckArrService(ctx, service, deps) },
			})
		}
	}
	return checks
}

// CheckArrService teste une instance Radarr ou Sonarr, déjà enregistrée ou candidate.
func CheckArrService(ctx context.Context, service config.ArrService, deps Deps) Result {
	err := deps.NewPinger(service).Ping(ctx)
	hint := message(i18n.KeyDoctorArrCheckURLHint)
	switch {
	case err == nil:
		return Result{Status: OK, Code: CodeOK, Detail: message(i18n.KeyDoctorArrOK)}
	case errors.Is(err, arr.ErrAccessDenied):
		return Result{
			Status: Fail, Code: CodeArrKeyRejected,
			Detail: message(i18n.KeyDoctorArrKeyRejected), Hint: message(i18n.KeyDoctorArrKeyRejectedHint),
		}
	case errors.Is(err, context.DeadlineExceeded):
		return Result{Status: Fail, Code: CodeArrTimeout, Detail: message(i18n.KeyDoctorArrTimeout), Hint: hint}
	}
	return Result{Status: Fail, Code: CodeArrUnreachable, Detail: message(i18n.KeyDoctorArrUnreachable, "error", err.Error()), Hint: hint}
}

// CheckTorrentClient teste un seul client torrent, déjà enregistré ou candidat.
func CheckTorrentClient(ctx context.Context, client config.TorrentClient, deps Deps) Result {
	status, code, detail, hint := inspectTorrentClient(ctx, 0, client, deps)
	return Result{Status: status, Code: code, Detail: detail, Hint: hint}
}

func checkQbittorrent(ctx context.Context, settings config.Config, deps Deps) Result {
	unsupported := settings.CrossSeedImport.UnsupportedTorrentClients
	if len(settings.TorrentClients) == 0 && len(unsupported) == 0 {
		return Result{Status: Skipped, Code: CodeNoTorrentClient, Detail: message(i18n.KeyDoctorQbitNone)}
	}
	var collected findings
	for clientIndex, client := range settings.TorrentClients {
		status, code, detail, hint := inspectTorrentClient(ctx, clientIndex, client, deps)
		collected.add(status, code, detail, hint)
	}
	if len(settings.TorrentClients) > 0 {
		collected.add(OK, CodeSeedSettings,
			message(i18n.KeyDoctorQbitSeeds, "category", settings.LinkCategory, "tags", strings.Join(settings.SeedTags, ", ")), i18n.Message{})
	}
	for _, ignored := range unsupported {
		collected.add(Warn, CodeClientTypeUnsupported,
			message(i18n.KeyDoctorQbitUnsupported, "index", strconv.Itoa(ignored.Index), "type", ignored.Type), message(i18n.KeyDoctorQbitUnsupportedHint))
	}
	return collected.result()
}

func inspectTorrentClient(ctx context.Context, clientIndex int, client config.TorrentClient, deps Deps) (Status, string, i18n.Message, i18n.Message) {
	index := strconv.Itoa(clientIndex)
	torrentClient, err := deps.NewTorrentClient(client)
	if err != nil {
		return Fail, CodeClientBuildFailed, message(i18n.KeyDoctorQbitBuildFailed, "index", index, "error", err.Error()),
			message(i18n.KeyDoctorQbitBuildFailedHint)
	}
	version, err := torrentClient.Version(ctx)
	if err != nil {
		return qbitVersionFailure(index, err)
	}
	count, atLeast, err := torrentClient.CountTorrents(ctx)
	if err != nil {
		return Fail, CodeClientListFailed, message(i18n.KeyDoctorQbitListFailed, "index", index, "error", err.Error()), i18n.Message{}
	}
	return OK, CodeClientOK, message(qbitOKKey(atLeast, client.ReadOnly), "index", index, "version", version, "count", strconv.Itoa(count)), i18n.Message{}
}

func qbitOKKey(atLeast, readOnly bool) i18n.Key {
	switch {
	case atLeast && readOnly:
		return i18n.KeyDoctorQbitOKAtLeastReadOnly
	case atLeast:
		return i18n.KeyDoctorQbitOKAtLeast
	case readOnly:
		return i18n.KeyDoctorQbitOKReadOnly
	}
	return i18n.KeyDoctorQbitOK
}

func qbitVersionFailure(index string, err error) (Status, string, i18n.Message, i18n.Message) {
	hint := message(i18n.KeyDoctorQbitCredentialsHint)
	switch {
	case errors.Is(err, qbit.ErrAuthFailed):
		return Fail, CodeClientLoginRejected, message(i18n.KeyDoctorQbitLoginRejected, "index", index), hint
	case errors.Is(err, context.DeadlineExceeded):
		return Fail, CodeClientTimeout, message(i18n.KeyDoctorQbitTimeout, "index", index), hint
	}
	return Fail, CodeClientUnreachable, message(i18n.KeyDoctorQbitUnreachable, "index", index, "error", err.Error()), hint
}

func checkCrossSeed(ctx context.Context, settings config.Config, deps Deps) Result {
	imported := settings.CrossSeedImport
	if !imported.Configured && settings.CrossSeed == nil {
		return Result{Status: Skipped, Code: CodeCrossSeedUnset, Detail: message(i18n.KeyDoctorCrossSeedNone)}
	}
	var details []i18n.Message
	if imported.Configured {
		details = append(details, message(i18n.KeyDoctorCrossSeedImported, "keys", strings.Join(imported.ImportedKeys, ", "),
			"ignored", strconv.Itoa(len(imported.IgnoredKeys)+imported.UnnamedIgnoredKeys)))
	}
	reachable := settings.CrossSeed != nil && deps.Reachable(ctx, settings.CrossSeed.BaseURL)
	details = append(details, daemonSummary(settings.CrossSeed != nil, reachable))
	behavior, warning, err := crossseed.ResolveMode(settings.CrossSeedMode, settings.CrossSeed != nil, reachable)
	if err != nil {
		return Result{
			Status: Fail, Code: CodeCrossSeedModeInvalid, Detail: message(i18n.KeyDoctorCrossSeedModeInvalid, "error", err.Error()),
			Hint: message(i18n.KeyDoctorCrossSeedModeInvalidHint, "modes", strings.Join(crossseed.AllowedModes, ", ")),
		}
	}
	details = append(details, message(behaviorKey(behavior), "mode", settings.CrossSeedMode))
	var hints []i18n.Message
	code := CodeCrossSeedWarning
	hasWarning := warning.Key != ""
	if hasWarning {
		hints = append(hints, warning)
	}
	if blocked := imported.BlockedEnvironment; len(blocked) > 0 {
		hints = append(hints, message(i18n.KeyDoctorCrossSeedBlockedEnv, "names", strings.Join(blocked, ", ")))
		if !hasWarning {
			code = CodeBlockedEnvironment
		}
	}
	if len(hints) > 0 {
		return Result{Status: Warn, Code: code, Details: details, Hints: hints}
	}
	return Result{Status: OK, Code: CodeOK, Details: details}
}

func daemonSummary(configured, reachable bool) i18n.Message {
	switch {
	case !configured:
		return message(i18n.KeyDoctorCrossSeedNoDaemon)
	case reachable:
		return message(i18n.KeyDoctorCrossSeedDaemonReachable)
	}
	return message(i18n.KeyDoctorCrossSeedDaemonUnreachable)
}

func behaviorKey(behavior crossseed.Behavior) i18n.Key {
	switch behavior {
	case crossseed.Delegate:
		return i18n.KeyDoctorCrossSeedModeDelegate
	case crossseed.Native:
		return i18n.KeyDoctorCrossSeedModeNative
	}
	return i18n.KeyDoctorCrossSeedModeDisabled
}
