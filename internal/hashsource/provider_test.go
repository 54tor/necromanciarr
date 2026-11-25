package hashsource

import (
	"context"
	"testing"

	"necromanciarr/internal/config"
)

type namedSource struct{ name string }

func (namedSource) Hash(context.Context, string, Algorithm) (Result, error) { return Result{}, nil }

func namedFactory(spec config.FSProvider, _ Deps) (HashSource, error) {
	return namedSource{name: spec.Name}, nil
}

func TestRegistry_forPathPicksLongestMountAcrossProviders(t *testing.T) {
	providers := []config.FSProvider{
		{Type: "fake", Name: "wide", Mounts: []config.FSMount{{LocalPath: "/home/u/media", Target: "a:"}}},
		{Type: "fake", Name: "narrow", Mounts: []config.FSMount{{LocalPath: "/home/u/media/remote", Target: "b:"}}},
	}
	registry, err := newRegistry(providers, Deps{}, map[string]ProviderFactory{"fake": namedFactory})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path         string
		expectedName string
	}{
		{"/home/u/media/remote/film.mkv", "narrow"},
		{"/home/u/media/other/film.mkv", "wide"},
		{"/home/u/media/remotestore/film.mkv", "wide"},
	}

	for _, testCase := range cases {
		source, found := registry.ForPath(testCase.path)

		if !found || source.(namedSource).name != testCase.expectedName {
			t.Fatalf("%s: gotValue %v %v, expectedValue %s", testCase.path, source, found, testCase.expectedName)
		}
	}
	for _, unmatched := range []string{"/home/u/media", "/elsewhere/a.mkv", "relative/a.mkv", "/home/u/media/../x"} {
		if _, found := registry.ForPath(unmatched); found {
			t.Fatalf("%s must not match", unmatched)
		}
	}
}

func TestNewFromConfig_buildsRcloneProvidersAndRejectsUnknownTypes(t *testing.T) {
	rclone := config.FSProvider{Type: "rclone", Name: "remote", RC: "http://rclone:5572", Mounts: []config.FSMount{{LocalPath: "/home/u/remote", Target: "r:x"}}}

	registry, err := NewFromConfig([]config.FSProvider{rclone}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.ForPath("/home/u/remote/a.mkv"); !found {
		t.Fatal("rclone mount not resolved")
	}
	if _, err := NewFromConfig([]config.FSProvider{{Type: "ftp", Name: "x"}}, Deps{}); err == nil {
		t.Fatal("unknown type must fail")
	}
}

// Un fournisseur sftp est un volume (sftp://…), pas une source d'empreintes : il ne doit pas empêcher le lancement.
func TestNewFromConfig_skipsSFTPFileSystemProviders(t *testing.T) {
	sftpProvider := config.FSProvider{Type: config.FSProviderTypeSFTP, Name: "seedbox", Address: "seedbox.example.com:22"}

	registry, err := NewFromConfig([]config.FSProvider{sftpProvider}, Deps{})

	if err != nil || len(registry.DescribeMounts(context.Background())) != 0 {
		t.Fatalf("valeurObtenue %v, valeurAttendue the sftp provider skipped without error", err)
	}
}

func TestRegistry_describeMountsNamesTheProviderOfEachMount(t *testing.T) {
	providers := []config.FSProvider{
		{Type: "fake", Name: "ignored", Mounts: []config.FSMount{{LocalPath: "/home/u/x", Target: "a:"}}},
		{Type: "rclone", Name: "remote", Binary: "/nonexistent/rclone-for-test", Mounts: []config.FSMount{{LocalPath: "/home/u/remote", Target: "r:x"}}},
	}
	registry, err := newRegistry(providers, Deps{}, map[string]ProviderFactory{"fake": namedFactory, "rclone": newRcloneProvider})
	if err != nil {
		t.Fatal(err)
	}

	gotValue := registry.DescribeMounts(context.Background())

	if len(gotValue) != 1 || gotValue[0].Provider != "remote" || gotValue[0].Mount.LocalPath != "/home/u/remote" {
		t.Fatalf("gotValue %+v", gotValue)
	}
}

type valueSource struct{ value string }

func (source valueSource) Hash(context.Context, string, Algorithm) (Result, error) {
	return Result{Value: source.value, Origin: Backend, Available: true}, nil
}

func TestRegistry_hashAsksTheMountOwnerAndHasNoHashOutsideMounts(t *testing.T) {
	providers := []config.FSProvider{{Type: "fixed", Name: "s3", Mounts: []config.FSMount{{LocalPath: "/home/u/media", Target: "s3:"}}}}
	fixedFactory := func(config.FSProvider, Deps) (HashSource, error) { return valueSource{value: "abc"}, nil }
	registry, err := newRegistry(providers, Deps{}, map[string]ProviderFactory{"fixed": fixedFactory})
	if err != nil {
		t.Fatal(err)
	}

	mounted, mountedErr := registry.Hash(context.Background(), "/home/u/media/film.mkv", MD5)
	outside, outsideErr := registry.Hash(context.Background(), "/home/u/other/film.mkv", MD5)

	if mountedErr != nil || !mounted.Available || mounted.Value != "abc" {
		t.Fatalf("mounted: gotValue %+v %v, expectedValue the provider hash", mounted, mountedErr)
	}
	if outsideErr != nil || outside.Available {
		t.Fatalf("outside: gotValue %+v %v, expectedValue no hash and no error", outside, outsideErr)
	}
}
