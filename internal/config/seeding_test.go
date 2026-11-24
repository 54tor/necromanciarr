package config

import (
	"slices"
	"strings"
	"testing"
)

func TestLoad_seedTagsDefaultToTheToolTag(t *testing.T) {
	expectedValue := []string{"necromanciarr"}

	gotValue := mustLoad(t, nil, nil, t.TempDir()).SeedTags

	if !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("gotValue %v, expectedValue %v", gotValue, expectedValue)
	}
}

func TestLoad_seedTagsFromFileReplaceTheDefaultTrimmedAndDeduplicated(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{"seedTags": [" qui ", "relinked", "qui"]}`)
	expectedValue := []string{"qui", "relinked"}

	gotValue := mustLoad(t, nil, nil, configDir).SeedTags

	if !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("gotValue %v, expectedValue %v", gotValue, expectedValue)
	}
}

func TestLoad_seedTagsEnvironmentOverridesTheFile(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{"seedTags": ["from-file"]}`)
	environment := map[string]string{envSeedTags: "necromanciarr, from-env"}
	expectedValue := []string{"necromanciarr", "from-env"}

	gotValue := mustLoad(t, nil, environment, configDir).SeedTags

	if !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("gotValue %v, expectedValue %v", gotValue, expectedValue)
	}
}

func TestLoad_seedTagsEmptyListKeepsTheDefault(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{"seedTags": []}`)

	gotValue := mustLoad(t, nil, nil, configDir).SeedTags

	if !slices.Equal(gotValue, []string{defaultSeedTag}) {
		t.Fatalf("gotValue %v, expectedValue the default tag", gotValue)
	}
}

func TestLoad_seedTagsRejectsInvalidEntriesNamingTheKey(t *testing.T) {
	tooMany := make([]string, maxSeedTags+1)
	for tagIndex := range tooMany {
		tooMany[tagIndex] = `"t` + strings.Repeat("x", tagIndex) + `"`
	}
	cases := map[string]string{
		"blank":    `{"seedTags": ["ok", "  "]}`,
		"comma":    `{"seedTags": ["a,b"]}`,
		"too long": `{"seedTags": ["` + strings.Repeat("x", maxSeedTagLength+1) + `"]}`,
		"too many": `{"seedTags": [` + strings.Join(tooMany, ",") + `]}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			configDir := t.TempDir()
			writeFile(t, configDir, configFileName, content)

			_, err := Load(nil, lookupFrom(nil), configDir)

			if err == nil || !strings.Contains(err.Error(), "seedTags") {
				t.Fatalf("gotValue %v, expectedValue an error naming seedTags", err)
			}
		})
	}
}

func TestLoad_seedTagsAcceptsExactlyTheLimits(t *testing.T) {
	atLimit := make([]string, maxSeedTags)
	for tagIndex := range atLimit {
		atLimit[tagIndex] = `"` + strings.Repeat("x", maxSeedTagLength-2) + string(rune('a'+tagIndex)) + `y"`
	}
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{"seedTags": [`+strings.Join(atLimit, ",")+`]}`)

	gotValue := mustLoad(t, nil, nil, configDir).SeedTags

	if len(gotValue) != maxSeedTags {
		t.Fatalf("gotValue %d tags, expectedValue %d", len(gotValue), maxSeedTags)
	}
}

func TestLoad_partialMaxMissingPercentDefaultsToNoCap(t *testing.T) {
	const expectedValue = 100.0

	gotValue := mustLoad(t, nil, nil, t.TempDir()).PartialMaxMissingPercent

	if gotValue != expectedValue {
		t.Fatalf("gotValue %v, expectedValue %v", gotValue, expectedValue)
	}
}

func TestLoad_partialMaxMissingPercentReadsZeroFromTheFileAndTheEnvironmentWins(t *testing.T) {
	configDir := t.TempDir()
	writeFile(t, configDir, configFileName, `{"partialMaxMissingPercent": 0}`)

	fromFile := mustLoad(t, nil, nil, configDir).PartialMaxMissingPercent
	fromEnv := mustLoad(t, nil, map[string]string{envPartialMaxMissingPercent: "12.5"}, configDir).PartialMaxMissingPercent

	if fromFile != 0 || fromEnv != 12.5 {
		t.Fatalf("gotValue file %v env %v, expectedValue 0 then 12.5", fromFile, fromEnv)
	}
}

func TestLoad_partialMaxMissingPercentRejectsOutOfRangeValuesNamingTheKey(t *testing.T) {
	for _, rawValue := range []string{"-1", "100.5", "abc", "NaN"} {
		t.Run(rawValue, func(t *testing.T) {
			environment := map[string]string{envPartialMaxMissingPercent: rawValue}

			_, err := Load(nil, lookupFrom(environment), t.TempDir())

			if err == nil || !strings.Contains(err.Error(), "partialMaxMissingPercent") {
				t.Fatalf("gotValue %v, expectedValue an error naming partialMaxMissingPercent", err)
			}
		})
	}
}
