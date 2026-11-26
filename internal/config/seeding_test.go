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

func TestLoad_seedTagsFromTheDatabaseReplaceTheDefaultTrimmedAndDeduplicated(t *testing.T) {
	stored := `{"seedTags": [" qui ", "relinked", "qui"]}`
	expectedValue := []string{"qui", "relinked"}

	gotValue := mustLoadStored(t, nil, nil, stored).SeedTags

	if !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("gotValue %v, expectedValue %v", gotValue, expectedValue)
	}
}

func TestLoad_seedTagsEnvironmentOverridesTheDatabase(t *testing.T) {
	stored := `{"seedTags": ["from-file"]}`
	environment := map[string]string{envSeedTags: "necromanciarr, from-env"}
	expectedValue := []string{"necromanciarr", "from-env"}

	gotValue := mustLoadStored(t, nil, environment, stored).SeedTags

	if !slices.Equal(gotValue, expectedValue) {
		t.Fatalf("gotValue %v, expectedValue %v", gotValue, expectedValue)
	}
}

func TestLoad_seedTagsEmptyListKeepsTheDefault(t *testing.T) {
	stored := `{"seedTags": []}`

	gotValue := mustLoadStored(t, nil, nil, stored).SeedTags

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
			_, err := loadStored(t, nil, nil, content)

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
	stored := `{"seedTags": [` + strings.Join(atLimit, ",") + `]}`

	gotValue := mustLoadStored(t, nil, nil, stored).SeedTags

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

func TestLoad_partialMaxMissingPercentReadsZeroFromTheDatabaseAndTheEnvironmentWins(t *testing.T) {
	stored := `{"partialMaxMissingPercent": 0}`

	fromFile := mustLoadStored(t, nil, nil, stored).PartialMaxMissingPercent
	fromEnv := mustLoadStored(t, nil, map[string]string{envPartialMaxMissingPercent: "12.5"}, stored).PartialMaxMissingPercent

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

func TestLoad_seedCategoryEnvironmentOverridesLinkCategoryFromTheDatabase(t *testing.T) {
	stored := `{"linkCategory": "from-file"}`
	const expectedValue = "from-env"

	fromFile := mustLoadStored(t, nil, nil, stored).LinkCategory
	gotValue := mustLoadStored(t, nil, map[string]string{envSeedCategory: expectedValue}, stored).LinkCategory

	if fromFile != "from-file" || gotValue != expectedValue {
		t.Fatalf("gotValue file %q env %q, expectedValue from-file then %q", fromFile, gotValue, expectedValue)
	}
}
