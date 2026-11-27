package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/pipeline"
)

// Sortes de volume de volumes list (contrat machine, jamais traduit).
const (
	VolumeTypeLocal = "local"
	VolumeTypeSFTP  = "sftp"
)

// Résultats de volumes probe.
const (
	VolumeProbeOK     = "ok"
	VolumeProbeFailed = "failed"
)

// ReasonNoLinkDir : le volume n'a aucun dossier de liens dans linkDirs ; la sonde n'a nulle part où écrire.
const ReasonNoLinkDir = "no_link_dir"

// reasonProbeFailed : la sonde a échoué pour une autre cause que les codes de connexion connus.
const reasonProbeFailed = "probe_failed"

// GuardScriptPath est le script de garde livré, qui n'admet que les lignes de volumes commands.
const GuardScriptPath = "docs/reference/remote-command-guard.sh"

// volumeProbeLockName nomme le verrou des étapes pris par volumes probe (elle écrit dans un dossier de liens).
const volumeProbeLockName = "volumes probe"

var (
	// ErrVolumeNotFound : aucun fournisseur local ou sftp de fsProviders ne porte ce nom.
	ErrVolumeNotFound = errors.New("app: sftp volume not found")
	// ErrVolumeProbeRunning : une sonde du même volume est déjà en cours dans ce processus.
	ErrVolumeProbeRunning = errors.New("app: a probe of this volume is already running")
)

// VolumeHandle est la part d'un volume sftp du registre que volumes list et volumes probe emploient ;
// *sftpfs.Volume convient.
type VolumeHandle interface {
	State() string
	Release()
	Capabilities(ctx context.Context) (fsys.Capabilities, error)
}

// VolumeCapabilities est la dernière sonde d'un volume : Identity est l'identité distante des fichiers (stat par
// ssh exec), Hashes les empreintes calculées sur l'hôte, Reasons les codes d'absence par fonction.
type VolumeCapabilities struct {
	Symlink       bool              `json:"symlink"`
	Hardlink      bool              `json:"hardlink"`
	AtomicReplace bool              `json:"atomic_replace"`
	ServerCopy    bool              `json:"server_copy"`
	Identity      bool              `json:"identity"`
	Hashes        []string          `json:"hashes"`
	Reasons       map[string]string `json:"reasons"`
}

// VolumeRow est une ligne de volumes list : Volume vide pour le disque local ; Address l'hôte et le port seuls ;
// State ok, broken ou unknown ; Capabilities et ProbedAt nil tant qu'aucune sonde n'est enregistrée.
type VolumeRow struct {
	Volume       string              `json:"volume"`
	Type         string              `json:"type"`
	Address      string              `json:"address"`
	State        string              `json:"state"`
	Capabilities *VolumeCapabilities `json:"capabilities"`
	ProbedAt     *time.Time          `json:"probed_at"`
}

// VolumeProbeResult rend un volumes probe : Result ok ou failed, Error le code stable de l'échec.
type VolumeProbeResult struct {
	Volume       string              `json:"volume"`
	Result       string              `json:"result"`
	Error        string              `json:"error,omitempty"`
	Capabilities *VolumeCapabilities `json:"capabilities,omitempty"`
	ProbedAt     *time.Time          `json:"probed_at,omitempty"`
}

// VolumeCommands rend les lignes ssh exec exactes (octet pour octet, tabulations comprises) qu'une garde distante
// doit admettre, et le script de garde livré.
type VolumeCommands struct {
	Volume      string   `json:"volume"`
	Lines       []string `json:"lines"`
	GuardScript string   `json:"guard_script"`
}

func (svc *service) ListVolumes(ctx context.Context) ([]VolumeRow, error) {
	rows := []VolumeRow{{Type: VolumeTypeLocal, State: sftpfs.StateOK}}
	for _, provider := range svc.localProviders() {
		row := VolumeRow{Volume: provider.Name, Type: VolumeTypeLocal, State: sftpfs.StateOK, Address: strings.Join(provider.Paths, ", ")}
		if err := svc.attachStoredProbe(ctx, &row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	for _, provider := range svc.sftpProviders() {
		row := VolumeRow{
			Volume: provider.Name, Type: VolumeTypeSFTP, State: svc.volumeState(provider.Name),
			Address: maskedAddress(provider.Address, svc.deps.Config.KnownSecrets()...),
		}
		if err := svc.attachStoredProbe(ctx, &row); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// attachStoredProbe joint à la ligne la dernière sonde enregistrée pour ce volume, s'il y en a une.
func (svc *service) attachStoredProbe(ctx context.Context, row *VolumeRow) error {
	capabilities, probedAt, found, err := svc.deps.Store.VolumeCapabilities(ctx, row.Volume)
	if err != nil || !found {
		return err
	}
	row.Capabilities, row.ProbedAt = capabilityView(capabilities), &probedAt
	return nil
}

func (svc *service) localProviders() []config.FSProvider {
	var providers []config.FSProvider
	for _, provider := range svc.deps.Config.FSProviders {
		if provider.Type == config.FSProviderTypeLocal {
			providers = append(providers, provider)
		}
	}
	return providers
}

func (svc *service) localProvider(name string) (config.FSProvider, bool) {
	for _, provider := range svc.localProviders() {
		if provider.Name == name {
			return provider, true
		}
	}
	return config.FSProvider{}, false
}

func (svc *service) sftpProviders() []config.FSProvider {
	var providers []config.FSProvider
	for _, provider := range svc.deps.Config.FSProviders {
		if provider.Type == config.FSProviderTypeSFTP {
			providers = append(providers, provider)
		}
	}
	return providers
}

func (svc *service) sftpProvider(name string) (config.FSProvider, bool) {
	for _, provider := range svc.sftpProviders() {
		if provider.Name == name {
			return provider, true
		}
	}
	return config.FSProvider{}, false
}

// volumeState : un volume absent du registre (jamais construit) est unknown, comme un volume jamais joint.
func (svc *service) volumeState(name string) string {
	volume, found := svc.deps.Volumes[name]
	if !found {
		return sftpfs.StateUnknown
	}
	return volume.State()
}

// maskedAddress ne garde que hôte:port : tout ce qui précède un @ (compte, mot de passe) disparaît, puis les secrets
// connus sont masqués. Une valeur qui ne se lit pas comme hôte:port n'est pas rendue.
func maskedAddress(address string, secrets ...string) string {
	if at := strings.LastIndex(address, "@"); at >= 0 {
		address = address[at+1:]
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	return config.RedactText(net.JoinHostPort(host, port), secrets...)
}

func capabilityView(capabilities fsys.Capabilities) *VolumeCapabilities {
	view := &VolumeCapabilities{
		Symlink: capabilities.Symlink, Hardlink: capabilities.Hardlink, AtomicReplace: capabilities.AtomicReplace,
		ServerCopy: capabilities.ServerCopy, Identity: capabilities.RemoteStat,
		Hashes: append([]string{}, capabilities.RemoteHash...), Reasons: map[string]string{},
	}
	for function, reason := range capabilities.Reasons {
		view.Reasons[function] = reason
	}
	return view
}

func (svc *service) VolumeCommands(_ context.Context, name string) (VolumeCommands, error) {
	if _, found := svc.sftpProvider(name); !found {
		return VolumeCommands{}, ErrVolumeNotFound
	}
	return VolumeCommands{Volume: name, Lines: sftpfs.GuardLines(), GuardScript: GuardScriptPath}, nil
}

// ProbeVolume sonde à nouveau le volume dans son premier dossier de liens, sous le verrou des étapes : la session
// est rendue d'abord (Release), puis Registry.WriteRoot rouvre une session et sonde dans cette racine, que le
// registre refuse si elle recouvre une bibliothèque. Le résultat est enregistré avec sa date ; la session est
// rendue à la fin. Un échec est un code stable, jamais le texte de l'erreur.
func (svc *service) ProbeVolume(ctx context.Context, name string) (VolumeProbeResult, error) {
	localProvider, isLocal := svc.localProvider(name)
	if _, isSFTP := svc.sftpProvider(name); !isSFTP && !isLocal {
		return VolumeProbeResult{}, ErrVolumeNotFound
	}
	if _, alreadyRunning := svc.volumeProbes.LoadOrStore(name, struct{}{}); alreadyRunning {
		return VolumeProbeResult{}, ErrVolumeProbeRunning
	}
	defer svc.volumeProbes.Delete(name)
	linkDir, hasLinkDir, err := svc.firstLinkDirForProbe(name, localProvider, isLocal)
	if err != nil {
		return VolumeProbeResult{}, err
	}
	if !hasLinkDir {
		return VolumeProbeResult{Volume: name, Result: VolumeProbeFailed, Error: ReasonNoLinkDir}, nil
	}
	var result VolumeProbeResult
	err = svc.locked(ctx, volumeProbeLockName, func(lockedCtx context.Context) error {
		if isLocal {
			result, err = svc.probeLocalIn(lockedCtx, name, linkDir)
		} else {
			result, err = svc.probeIn(lockedCtx, name, linkDir)
		}
		return err
	})
	return result, err
}

func (svc *service) firstLinkDirForProbe(name string, localProvider config.FSProvider, isLocal bool) (fsys.Location, bool, error) {
	if isLocal {
		return firstLocalLinkDirUnder(svc.deps.Config, localProvider)
	}
	return firstLinkDirOf(svc.deps.Config, name)
}

// probeLocalIn mesure les liens pour de vrai dans le dossier de liens, que le registre refuse s'il recouvre une
// bibliothèque ; la sonde ne laisse aucun fichier.
func (svc *service) probeLocalIn(ctx context.Context, name string, linkDir fsys.Location) (VolumeProbeResult, error) {
	registry := svc.deps.Stages.Registry
	if registry == nil {
		return VolumeProbeResult{}, fmt.Errorf("%w: local volume %s", ErrNotConfigured, name)
	}
	root, err := registry.WriteRoot(linkDir)
	if err != nil {
		return VolumeProbeResult{Volume: name, Result: VolumeProbeFailed, Error: volumeProbeErrorCode(err)}, nil
	}
	capabilities, probeErr := local.ProbeLinks(root)
	if failure := errors.Join(probeErr, root.Close()); failure != nil {
		return VolumeProbeResult{Volume: name, Result: VolumeProbeFailed, Error: volumeProbeErrorCode(failure)}, nil
	}
	probedAt := svc.now()
	if err := svc.deps.Store.SaveVolumeCapabilities(ctx, name, capabilities, probedAt); err != nil {
		return VolumeProbeResult{}, err
	}
	return VolumeProbeResult{Volume: name, Result: VolumeProbeOK, Capabilities: capabilityView(capabilities), ProbedAt: &probedAt}, nil
}

func (svc *service) probeIn(ctx context.Context, name string, linkDir fsys.Location) (VolumeProbeResult, error) {
	volume, found := svc.deps.Volumes[name]
	registry := svc.deps.Stages.Registry
	if !found || registry == nil {
		return VolumeProbeResult{}, fmt.Errorf("%w: sftp volume %s", ErrNotConfigured, name)
	}
	volume.Release()
	defer volume.Release()
	root, err := registry.WriteRoot(linkDir)
	if err != nil {
		return VolumeProbeResult{Volume: name, Result: VolumeProbeFailed, Error: volumeProbeErrorCode(err)}, nil
	}
	_ = root.Close()
	capabilities, err := volume.Capabilities(ctx)
	if err != nil {
		return VolumeProbeResult{Volume: name, Result: VolumeProbeFailed, Error: volumeProbeErrorCode(err)}, nil
	}
	probedAt := svc.now()
	if err := svc.deps.Store.SaveVolumeCapabilities(ctx, name, capabilities, probedAt); err != nil {
		return VolumeProbeResult{}, err
	}
	return VolumeProbeResult{Volume: name, Result: VolumeProbeOK, Capabilities: capabilityView(capabilities), ProbedAt: &probedAt}, nil
}

// firstLinkDirOf rend le premier dossier de linkDirs posé sur ce volume.
func firstLinkDirOf(settings config.Config, name string) (fsys.Location, bool, error) {
	for _, configured := range settings.LinkDirs {
		location, err := fsys.ParseLocation(configured)
		if err != nil {
			return fsys.Location{}, false, fmt.Errorf("app: linkDirs %q: %w", configured, err)
		}
		if location.Volume == name {
			return location, true, nil
		}
	}
	return fsys.Location{}, false, nil
}

// firstLocalLinkDirUnder rend le premier dossier de linkDirs du disque local posé sous un dossier du volume local.
func firstLocalLinkDirUnder(settings config.Config, provider config.FSProvider) (fsys.Location, bool, error) {
	for _, configured := range settings.LinkDirs {
		location, err := fsys.ParseLocation(configured)
		if err != nil {
			return fsys.Location{}, false, fmt.Errorf("app: linkDirs %q: %w", configured, err)
		}
		if location.Volume == "" && isUnderAnyPath(location.Path, provider.Paths) {
			return location, true, nil
		}
	}
	return fsys.Location{}, false, nil
}

func isUnderAnyPath(path string, roots []string) bool {
	for _, root := range roots {
		relative, err := filepath.Rel(root, path)
		if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// volumeProbeErrorCode réduit l'échec d'une sonde à un code stable : code de connexion sftpfs (clé d'hôte,
// authentification, known_hosts), volume injoignable, exec required, dossier sur une bibliothèque, sinon probe_failed.
func volumeProbeErrorCode(err error) string {
	if code := sftpfs.Code(err); code != "" {
		return code
	}
	switch {
	case errors.Is(err, fsys.ErrRemoteUnavailable):
		return pipeline.ReasonRemoteUnavailable
	case errors.Is(err, fsys.ErrRemoteExecRequired):
		return pipeline.ReasonRemoteExecRequired
	case errors.Is(err, fsys.ErrWriteInLibrary):
		return pipeline.ReasonLinkDirOverlapsLibrary
	default:
		return reasonProbeFailed
	}
}

// brokenVolumes compte les volumes sftp sans session (pastille de la vue Volumes).
func (svc *service) brokenVolumes() int {
	broken := 0
	for _, volume := range svc.deps.Volumes {
		if volume.State() == sftpfs.StateBroken {
			broken++
		}
	}
	return broken
}
