package doctor

import (
	"context"
	"strings"
	"testing"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/linker"
)

func aliasDeps() Deps {
	return Deps{
		Probe:      func(string) (linker.Capabilities, error) { return linker.Capabilities{Hardlink: true}, nil },
		SameVolume: func(string, string) (bool, error) { return true, nil },
	}.withDefaults()
}

// aliasDepsOnADetectingKernel garde les contrôles d'alias indépendants du noyau qui fait tourner le test.
func aliasDepsOnADetectingKernel(t *testing.T, settings config.Config) Deps {
	t.Helper()
	deps := aliasDeps()
	deps.Registry = registryWithLocal(t, detectingVolume{local.New()}, settings)
	return deps
}

func sftpProviderAt(name, address string) config.FSProvider {
	return config.FSProvider{Type: config.FSProviderTypeSFTP, Name: name, Address: address}
}

func localLinkSettings(t *testing.T, providers ...config.FSProvider) config.Config {
	t.Helper()
	return config.Config{
		LibraryRoots: []string{t.TempDir()}, LinkDirs: []string{t.TempDir()}, LinkType: "hardlink", FSProviders: providers,
	}
}

func TestChecks_linkFeasibilityWarnsAboutALoopbackSFTPVolumeNextToLocalLibraryRoots(t *testing.T) {
	for _, address := range []string{"localhost:22", "127.0.0.1:2222", "[::1]:22"} {
		settings := localLinkSettings(t, sftpProviderAt("box", address))

		got := checkLinkFeasibility(settings, aliasDepsOnADetectingKernel(t, settings))

		if got.Status != Warn || got.Code != CodeVolumeAliasRisk || !strings.Contains(detailOf(got), "same disk") {
			t.Errorf("%s: valeurObtenue %s/%s %q, valeurAttendue warn/%s", address, got.Status, got.Code, detailOf(got), CodeVolumeAliasRisk)
		}
	}
}

func TestChecks_linkFeasibilityGivesNoAliasWarningForARemoteSFTPVolume(t *testing.T) {
	settings := localLinkSettings(t, sftpProviderAt("box", "seedbox.example.com:22"))

	got := checkLinkFeasibility(settings, aliasDepsOnADetectingKernel(t, settings))

	if got.Status != OK || strings.Contains(detailOf(got), "same disk") || strings.Contains(detailOf(got), "same address") {
		t.Fatalf("valeurObtenue %s/%s %q, valeurAttendue ok without an alias line", got.Status, got.Code, detailOf(got))
	}
}

func TestChecks_linkFeasibilityNotesProvidersSharingOneAddress(t *testing.T) {
	settings := localLinkSettings(t, sftpProviderAt("seedbox", "Seedbox.example.com:22"), sftpProviderAt("mirror", "seedbox.example.com."))

	got := checkLinkFeasibility(settings, aliasDepsOnADetectingKernel(t, settings))

	if got.Status != OK || !strings.Contains(detailOf(got), "same address") || !strings.Contains(detailOf(got), "mirror") {
		t.Fatalf("valeurObtenue %s/%s %q, valeurAttendue ok with the shared-address line", got.Status, got.Code, detailOf(got))
	}
}

// wrappedVolume nomme le champ imbriqué autrement que la méthode Volume() qu'il promeut.
type wrappedVolume = fsys.Volume

// oldKernelVolume est le disque local d'un noyau sans STATX_ATTR_MOUNT_ROOT (Linux avant 5.8).
type oldKernelVolume struct{ wrappedVolume }

func (volume oldKernelVolume) Capabilities(ctx context.Context) (fsys.Capabilities, error) {
	capabilities, err := volume.wrappedVolume.Capabilities(ctx)
	reasons := map[string]string{}
	for key, value := range capabilities.Reasons {
		reasons[key] = value
	}
	reasons[local.ReasonKeyMountRoot] = local.ReasonMountRootUnsupported
	capabilities.Reasons = reasons
	return capabilities, err
}

func registryWithLocal(t *testing.T, volume fsys.Volume, settings config.Config) *fsys.Registry {
	t.Helper()
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": volume}, []fsys.Location{{Path: settings.LibraryRoots[0]}})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func TestChecks_linkFeasibilityWarnsWhenTheKernelCannotDetectMountRoots(t *testing.T) {
	settings := localLinkSettings(t)
	deps := aliasDeps()
	deps.Registry = registryWithLocal(t, oldKernelVolume{local.New()}, settings)

	got := checkLinkFeasibility(settings, deps)

	if got.Status != Warn || got.Code != CodeMountRootUndetected {
		t.Fatalf("valeurObtenue %s/%s %q, valeurAttendue warn/%s", got.Status, got.Code, detailOf(got), CodeMountRootUndetected)
	}
}

func TestChecks_linkFeasibilityStaysQuietWhenMountRootsAreDetected(t *testing.T) {
	settings := localLinkSettings(t)
	deps := aliasDeps()
	deps.Registry = registryWithLocal(t, detectingVolume{local.New()}, settings)

	got := checkLinkFeasibility(settings, deps)

	if got.Code == CodeMountRootUndetected {
		t.Fatalf("valeurObtenue %s/%s, valeurAttendue no %s on a kernel that detects mount roots", got.Status, got.Code, CodeMountRootUndetected)
	}
}

// detectingVolume est le disque local d'un noyau qui détecte les racines de montage (aucune raison mountRoot).
type detectingVolume struct{ wrappedVolume }

func (volume detectingVolume) Capabilities(ctx context.Context) (fsys.Capabilities, error) {
	capabilities, err := volume.wrappedVolume.Capabilities(ctx)
	reasons := map[string]string{}
	for key, value := range capabilities.Reasons {
		if key != local.ReasonKeyMountRoot {
			reasons[key] = value
		}
	}
	capabilities.Reasons = reasons
	return capabilities, err
}
