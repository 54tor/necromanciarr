package doctor

import (
	"context"
	"slices"
	"strings"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/i18n"
)

// addVolumeAliasFindings signale ce que le registre ne rapproche pas tout seul : un fournisseur sftp en bouclage
// (localhost, 127.0.0.0/8, ::1) à côté de racines de bibliothèque locales peut viser le même disque qu'un chemin
// local (avertissement) ; des fournisseurs de même adresse partagent leurs racines de bibliothèque (information).
// Un nom DNS et son adresse IP ne sont pas rapprochés.
func addVolumeAliasFindings(collected *findings, settings config.Config) {
	hasLocalLibraryRoot := slices.ContainsFunc(settings.LibraryRoots, isLocalLocation)
	var addressKeys []string
	providersByKey := map[string][]config.FSProvider{}
	for _, provider := range settings.FSProviders {
		if provider.Type != config.FSProviderTypeSFTP {
			continue
		}
		if hasLocalLibraryRoot && config.IsLoopbackSFTPAddress(provider.Address) {
			collected.add(Warn, CodeVolumeAliasRisk,
				message(i18n.KeyDoctorVolumeAliasLoopback, "name", provider.Name, "address", provider.Address),
				message(i18n.KeyDoctorVolumeAliasLoopbackHint))
		}
		key, err := config.SFTPAddressKey(provider.Address)
		if err != nil {
			continue
		}
		if _, seen := providersByKey[key]; !seen {
			addressKeys = append(addressKeys, key)
		}
		providersByKey[key] = append(providersByKey[key], provider)
	}
	for _, key := range addressKeys {
		addSharedAddressFinding(collected, providersByKey[key])
	}
}

func addSharedAddressFinding(collected *findings, providers []config.FSProvider) {
	if len(providers) < 2 {
		return
	}
	names := make([]string, 0, len(providers))
	for _, provider := range providers {
		names = append(names, provider.Name)
	}
	detail := message(i18n.KeyDoctorVolumeAliasShared, "names", strings.Join(names, ", "), "address", providers[0].Address)
	collected.add(OK, CodeVolumeAliasRisk, detail, i18n.Message{})
}

func isLocalLocation(configured string) bool {
	location, err := fsys.StoredLocation(configured)
	return err == nil && location.Volume == ""
}

// addMountRootFinding avertit quand le disque local ne sait pas reconnaître une racine de montage (Linux avant 5.8) :
// un montage lié du même système de fichiers posé sous un dossier de liens local passerait le contrôle d'I1.
func addMountRootFinding(ctx context.Context, collected *findings, registry *fsys.Registry, settings config.Config) {
	if !slices.ContainsFunc(linkDirectories(settings), isLocalLocation) {
		return
	}
	reader, _, err := registry.Read(fsys.Location{})
	if err != nil {
		return
	}
	capabilities, err := reader.Capabilities(ctx)
	if err != nil || capabilities.Reasons[local.ReasonKeyMountRoot] != local.ReasonMountRootUnsupported {
		return
	}
	collected.add(Warn, CodeMountRootUndetected, message(i18n.KeyDoctorMountRootUndetected), message(i18n.KeyDoctorMountRootUndetectedHint))
}
