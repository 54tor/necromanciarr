package doctor

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"slices"
	"strings"

	"github.com/pkg/sftp"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/i18n"
)

// Identifiants des contrôles par fournisseur sftp : « <préfixe>:<nom du fournisseur> ».
const (
	IDSFTP        = "sftp"
	IDSFTPLibrary = "sftp-library"
	IDSFTPExec    = "sftp-exec"
)

const (
	remoteExecReasonKey = "remoteExec"
	// Un mode d'écriture du groupe ou des autres (bits 0o020 et 0o002) suffit à juger la bibliothèque modifiable.
	groupOrOtherWriteBits = 0o022
	ownerWriteBit         = 0o200
)

// windowsDrivePath reconnaît « /C: » seul ou suivi d'un segment : la forme que rend un serveur OpenSSH Windows. Le
// deux-points est obligatoire, pour qu'un dossier Unix nommé /C ou /c n'en soit jamais un.
var windowsDrivePath = regexp.MustCompile(`^/[A-Za-z]:(/|$)`)

func isWindowsDrivePath(candidate string) bool { return windowsDrivePath.MatchString(candidate) }

// RemoteVolume est ce que le diagnostic lit d'un volume sftp (un *sftpfs.Volume) ; Release ferme sa session sans fermer
// le volume : aucune connexion ne survit au diagnostic.
type RemoteVolume interface {
	ProbeExec(ctx context.Context) (sftpfs.ExecStatus, error)
	Warnings() []sftpfs.Warning
	Capabilities(ctx context.Context) (fsys.Capabilities, error)
	Release()
}

// sftpChecks rend, pour chaque fournisseur sftp déclaré, la connexion, la bibliothèque distante et l'exec
// distant. Tous passent par le volume paresseux du registre : aucun chemin de connexion de plus.
func sftpChecks(settings config.Config, deps Deps) []Check {
	var checks []Check
	for _, provider := range settings.FSProviders {
		if provider.Type != config.FSProviderTypeSFTP {
			continue
		}
		titleParams := map[string]string{"name": provider.Name}
		checks = append(checks,
			Check{
				ID: IDSFTP + ":" + provider.Name, TitleKey: i18n.KeyDoctorTitleSFTP, TitleParams: titleParams,
				Run: func(context.Context) Result { return checkSFTP(settings, deps, provider) },
			},
			Check{
				ID: IDSFTPLibrary + ":" + provider.Name, TitleKey: i18n.KeyDoctorTitleSFTPLibrary, TitleParams: titleParams,
				Run: func(ctx context.Context) Result { return checkSFTPLibrary(ctx, settings, deps, provider) },
			},
			Check{
				ID: IDSFTPExec + ":" + provider.Name, TitleKey: i18n.KeyDoctorTitleSFTPExec, TitleParams: titleParams,
				Run: func(ctx context.Context) Result { return checkSFTPExec(ctx, settings, deps, provider) },
			},
		)
	}
	return checks
}

// remoteLocations rend, parmi les chemins configurés, ceux qui visent ce fournisseur.
func remoteLocations(configured []string, provider string) []fsys.Location {
	var locations []fsys.Location
	for _, raw := range configured {
		location, err := fsys.StoredLocation(raw)
		if err == nil && location.Volume == provider {
			locations = append(locations, location)
		}
	}
	return locations
}

// connectionCause range une erreur de connexion par sa classe typée (errors.Is, jamais le texte) : code stable,
// libellé de la cause et conseil. Aucun texte d'erreur n'est repris : il peut citer le serveur ou un chemin.
type connectionCause struct {
	sentinel error
	code     string
	reason   i18n.Key
	hint     i18n.Key
}

var connectionCauses = []connectionCause{
	{sftpfs.ErrHostKeyUnknown, CodeHostKeyUnknown, i18n.KeyReasonHostKeyUnknown, i18n.KeyDoctorSFTPHintKnownHosts},
	{sftpfs.ErrHostKeyMismatch, CodeHostKeyMismatch, i18n.KeyReasonHostKeyMismatch, i18n.KeyDoctorSFTPHintHostKeyChanged},
	{sftpfs.ErrAuthRefused, CodeAuthRefused, i18n.KeyReasonAuthRefused, i18n.KeyDoctorSFTPHintAuth},
	{sftpfs.ErrKnownHostsUnreadable, CodeKnownHostsUnreadable, i18n.KeyReasonKnownHostsUnreadable, i18n.KeyDoctorSFTPHintKnownHosts},
	{sftpfs.ErrIdentityKeyTooWeak, CodeIdentityKeyTooWeak, i18n.KeyReasonIdentityKeyTooWeak, i18n.KeyDoctorSFTPHintIdentity},
	{sftpfs.ErrIdentityPassphraseMissing, CodeIdentityFileUnusable, i18n.KeyReasonIdentityFileUnusable, i18n.KeyDoctorSFTPHintIdentity},
	{sftpfs.ErrIdentityUnreadable, CodeIdentityFileUnusable, i18n.KeyReasonIdentityFileUnusable, i18n.KeyDoctorSFTPHintIdentity},
	{sftpfs.ErrHostileResponse, CodeHostileResponse, i18n.KeyReasonHostileResponse, i18n.KeyDoctorSFTPHintHostile},
}

var unavailableCause = connectionCause{
	fsys.ErrRemoteUnavailable, CodeRemoteUnavailable, i18n.KeyReasonRemoteUnavailable, i18n.KeyDoctorSFTPHintUnavailable,
}

func causeOf(err error) connectionCause {
	for _, cause := range connectionCauses {
		if errors.Is(err, cause.sentinel) {
			return cause
		}
	}
	return unavailableCause
}

func connectionFailure(err error, subject i18n.Message) Result {
	cause := causeOf(err)
	return Result{
		Status: Fail, Code: cause.code,
		Details: []i18n.Message{subject, message(cause.reason)}, Hints: []i18n.Message{message(cause.hint)},
	}
}

func checkSFTP(settings config.Config, deps Deps, provider config.FSProvider) Result {
	subject := message(i18n.KeyDoctorSFTPTarget, "name", provider.Name, "address", provider.Address, "user", provider.User)
	registry := deps.registryFor(settings)
	reader, _, err := registry.Read(fsys.Location{Volume: provider.Name, Path: "/"})
	if err != nil {
		return connectionFailure(err, subject)
	}
	root, err := reader.Canonical("/")
	if err != nil {
		return connectionFailure(err, subject)
	}
	if isWindowsServer(reader, root, settings, provider.Name) {
		return Result{
			Status: Fail, Code: CodeRemoteWindowsUnsupported,
			Details: []i18n.Message{subject, message(i18n.KeyReasonRemoteWindowsUnsupported)}, Hints: []i18n.Message{message(i18n.KeyDoctorSFTPHintWindows)},
		}
	}
	var collected findings
	collected.add(OK, CodeSFTPOK, message(i18n.KeyDoctorSFTPOK, "name", provider.Name, "address", provider.Address, "user", provider.User), i18n.Message{})
	if remote, known := deps.RemoteVolumes[provider.Name]; known {
		addConnectionWarnings(&collected, remote.Warnings())
	}
	return collected.result()
}

// isWindowsServer : RealPath("/") ou un dossier configuré du fournisseur rend « /C:/… ». Seule la forme à deux-points
// compte : un dossier Unix nommé /C n'est pas un lecteur.
func isWindowsServer(reader fsys.ReadFS, rootAnswer string, settings config.Config, provider string) bool {
	if isWindowsDrivePath(rootAnswer) {
		return true
	}
	configured := slices.Concat(settings.LibraryRoots, linkDirectories(settings))
	for _, location := range remoteLocations(configured, provider) {
		canonical, err := reader.Canonical(location.Path)
		if err == nil && isWindowsDrivePath(canonical) {
			return true
		}
	}
	return false
}

var connectionWarnings = map[string]struct {
	reason, hint i18n.Key
}{
	sftpfs.CodeIdentityFilePermissions: {i18n.KeyReasonIdentityFilePermissions, i18n.KeyDoctorSFTPHintChmod},
	sftpfs.CodeAgentUnavailable:        {i18n.KeyReasonAgentUnavailable, i18n.KeyDoctorSFTPHintAgent},
}

func addConnectionWarnings(collected *findings, warnings []sftpfs.Warning) {
	for _, warning := range warnings {
		known, isConnectionWarning := connectionWarnings[warning.Code]
		if isConnectionWarning {
			collected.add(Warn, warning.Code, message(known.reason), message(known.hint))
		}
	}
}

func checkSFTPLibrary(ctx context.Context, settings config.Config, deps Deps, provider config.FSProvider) Result {
	roots := remoteLocations(settings.LibraryRoots, provider.Name)
	if len(roots) == 0 {
		return Result{Status: Skipped, Code: CodeNoLibraryRoot, Detail: message(i18n.KeyDoctorSFTPLibraryNone, "name", provider.Name)}
	}
	registry := deps.registryFor(settings)
	accountOwner, accountKnown := accountOwnerOf(registry, settings, provider.Name)
	var collected findings
	for _, root := range roots {
		inspectRemoteLibrary(ctx, &collected, registry, root, accountOwner, accountKnown)
	}
	return collected.result()
}

// accountOwnerOf devine l'identifiant du compte SFTP : propriétaire du premier dossier de liens du fournisseur, où le
// compte écrit. Sans dossier de liens lisible, il reste inconnu et seuls les bits de groupe et d'autres comptent.
func accountOwnerOf(registry *fsys.Registry, settings config.Config, provider string) (uint32, bool) {
	for _, location := range remoteLocations(linkDirectories(settings), provider) {
		reader, name, err := registry.Read(location)
		if err != nil {
			continue
		}
		info, err := reader.Stat(name)
		if err != nil {
			continue
		}
		if stat, hasStat := info.Sys().(*sftp.FileStat); hasStat {
			return stat.UID, true
		}
	}
	return 0, false
}

func inspectRemoteLibrary(ctx context.Context, collected *findings, registry *fsys.Registry, root fsys.Location, accountOwner uint32, accountKnown bool) {
	subject := message(i18n.KeyDoctorSFTPLibraryRoot, "root", root.String())
	unreadable := func(code string, reason i18n.Key) {
		collected.add(Fail, code, subject, message(i18n.KeyDoctorSFTPLibraryHintUnread))
		collected.details = append(collected.details, message(reason))
	}
	reader, name, err := registry.Read(root)
	if err != nil {
		unreadable(CodeRemoteUnavailable, i18n.KeyReasonRemoteUnavailable)
		return
	}
	resolved, err := reader.Canonical(name)
	if errors.Is(err, fsys.ErrRemoteUnavailable) {
		unreadable(CodeRemoteUnavailable, i18n.KeyReasonRemoteUnavailable)
		return
	}
	if err != nil {
		unreadable(CodeLibraryRootUnreadable, i18n.KeyReasonLibraryRootUnreadable)
		return
	}
	info, err := reader.Stat(resolved)
	if err != nil || !info.IsDir() {
		unreadable(CodeLibraryRootUnreadable, i18n.KeyReasonLibraryRootUnreadable)
		return
	}
	walk := findMediaFile(ctx, reader, resolved)
	if walk.rootUnreadable {
		unreadable(CodeLibraryRootUnreadable, i18n.KeyReasonLibraryRootUnreadable)
		return
	}
	if walk.found {
		collected.add(OK, CodeRootOK, message(i18n.KeyDoctorSFTPLibraryOK, "root", root.String()), i18n.Message{})
	} else {
		collected.add(Warn, CodeNoMediaInFirst2000, subject, message(i18n.KeyDoctorSFTPLibraryHintNoMedia))
		collected.details = append(collected.details, message(i18n.KeyReasonNoMediaInFirst2000))
	}
	if looksWritable(info, accountOwner, accountKnown) {
		collected.add(Warn, CodeLibraryMaybeWritable, subject, message(i18n.KeyDoctorSFTPLibraryHintReadOnly))
		collected.details = append(collected.details, message(i18n.KeyReasonLibraryMaybeWritable))
	}
}

// looksWritable juge, sur le mode et le propriétaire que le serveur annonce, que le compte peut sans doute écrire dans
// le dossier : jamais par un essai d'écriture. Le bit d'écriture du groupe compte sans connaître les groupes du compte.
func looksWritable(info fs.FileInfo, accountOwner uint32, accountKnown bool) bool {
	permissions := info.Mode().Perm()
	if permissions&groupOrOtherWriteBits != 0 {
		return true
	}
	stat, hasStat := info.Sys().(*sftp.FileStat)
	return hasStat && accountKnown && stat.UID == accountOwner && permissions&ownerWriteBit != 0
}

func checkSFTPExec(ctx context.Context, settings config.Config, deps Deps, provider config.FSProvider) Result {
	if provider.Exec == config.SFTPExecOff {
		return Result{Status: OK, Code: CodeExecOff, Detail: message(i18n.KeyDoctorSFTPExecOff, "name", provider.Name)}
	}
	remote, known := deps.RemoteVolumes[provider.Name]
	if !known {
		return connectionFailure(fsys.ErrUnknownVolume, message(i18n.KeyDoctorSFTPTarget, "name", provider.Name, "address", provider.Address, "user", provider.User))
	}
	status, err := remote.ProbeExec(ctx)
	switch {
	case errors.Is(err, fsys.ErrRemoteExecRequired):
		return Result{
			Status: Fail, Code: CodeRemoteExecRequiredFailed,
			Details: []i18n.Message{reasonMessage(execReasonOf(status, err))}, Hints: []i18n.Message{message(i18n.KeyDoctorSFTPExecHintRequired)},
		}
	case err != nil:
		return connectionFailure(err, message(i18n.KeyDoctorSFTPTarget, "name", provider.Name, "address", provider.Address, "user", provider.User))
	case !status.Enabled:
		return Result{
			Status: Warn, Code: CodeRemoteExecUnavailable,
			Details: []i18n.Message{reasonMessage(status.Reason)}, Hints: []i18n.Message{message(i18n.KeyDoctorSFTPExecHintUnavailable)},
		}
	}
	var collected findings
	collected.add(OK, CodeExecOK, message(i18n.KeyDoctorSFTPExecOK, "name", provider.Name, "hashes", strings.Join(status.Hashes, " ")), i18n.Message{})
	collected.details = append(collected.details, lowPriorityMessage(status))
	addCapabilityFindings(ctx, &collected, settings, deps, provider, remote)
	return collected.result()
}

func lowPriorityMessage(status sftpfs.ExecStatus) i18n.Message {
	if status.LowPriority {
		return message(i18n.KeyDoctorSFTPExecLowPriority)
	}
	return message(i18n.KeyDoctorSFTPExecNoLowPriority)
}

// reasonMessage est le libellé, sans paramètre, d'un code de raison ; un code qui n'a pas de libellé s'affiche tel quel.
func reasonMessage(code string) i18n.Message { return message(i18n.Key("reason." + code)) }

func execReasonOf(status sftpfs.ExecStatus, err error) string {
	var disabled *sftpfs.ExecDisabledError
	if errors.As(err, &disabled) {
		return disabled.Reason
	}
	return status.Reason
}

// addCapabilityFindings sonde les capacités par le premier dossier de liens du fournisseur, ouvert par le registre
// (qui sonde avant de rendre la racine) : jamais une racine de bibliothèque, que le registre refuse. Sans dossier de
// liens, les capacités restent non sondées et rien n'est écrit.
func addCapabilityFindings(ctx context.Context, collected *findings, settings config.Config, deps Deps, provider config.FSProvider, remote RemoteVolume) {
	registry := deps.registryFor(settings)
	probed := false
	var openErr error
	for _, location := range remoteLocations(linkDirectories(settings), provider.Name) {
		root, err := registry.WriteRoot(location)
		if err != nil {
			if !errors.Is(err, fsys.ErrWriteInLibrary) {
				openErr = err
			}
			continue
		}
		_ = root.Close()
		probed = true
		break
	}
	if !probed && openErr != nil {
		collected.add(Warn, CodeCapabilityProbeFailed, reasonMessage("probe_failed"), message(i18n.KeyDoctorSFTPHintUnavailable))
		return
	}
	if !probed {
		collected.details = append(collected.details, message(i18n.KeyDoctorSFTPExecNoLinkDir))
		return
	}
	capabilities, err := remote.Capabilities(ctx)
	if err != nil {
		return
	}
	if reason := capabilities.Reasons[remoteExecReasonKey]; reason != "" {
		collected.add(Warn, CodeRemoteExecUnavailable, reasonMessage(reason), message(i18n.KeyDoctorSFTPExecHintUnavailable))
	}
}
