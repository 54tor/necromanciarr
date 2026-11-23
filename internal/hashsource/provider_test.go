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
		{Type: "fake", Name: "narrow", Mounts: []config.FSMount{{LocalPath: "/home/u/media/mega", Target: "b:"}}},
	}
	registry, err := newRegistry(providers, Deps{}, map[string]ProviderFactory{"fake": namedFactory})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path         string
		expectedName string
	}{
		{"/home/u/media/mega/film.mkv", "narrow"},
		{"/home/u/media/other/film.mkv", "wide"},
		{"/home/u/media/megastore/film.mkv", "wide"},
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
	rclone := config.FSProvider{Type: "rclone", Name: "mega", RC: "http://rclone:5572", Mounts: []config.FSMount{{LocalPath: "/home/u/mega", Target: "r:x"}}}

	registry, err := NewFromConfig([]config.FSProvider{rclone}, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if _, found := registry.ForPath("/home/u/mega/a.mkv"); !found {
		t.Fatal("rclone mount not resolved")
	}
	if _, err := NewFromConfig([]config.FSProvider{{Type: "ftp", Name: "x"}}, Deps{}); err == nil {
		t.Fatal("unknown type must fail")
	}
}

func TestRegistry_describeMountsNamesTheProviderOfEachMount(t *testing.T) {
	providers := []config.FSProvider{
		{Type: "fake", Name: "ignored", Mounts: []config.FSMount{{LocalPath: "/home/u/x", Target: "a:"}}},
		{Type: "rclone", Name: "mega", Binary: "/nonexistent/rclone-for-test", Mounts: []config.FSMount{{LocalPath: "/home/u/mega", Target: "r:x"}}},
	}
	registry, err := newRegistry(providers, Deps{}, map[string]ProviderFactory{"fake": namedFactory, "rclone": newRcloneProvider})
	if err != nil {
		t.Fatal(err)
	}

	gotValue := registry.DescribeMounts(context.Background())

	if len(gotValue) != 1 || gotValue[0].Provider != "mega" || gotValue[0].Mount.LocalPath != "/home/u/mega" {
		t.Fatalf("gotValue %+v", gotValue)
	}
}
