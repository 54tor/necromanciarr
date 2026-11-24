package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"necromanciarr/internal/config"
	"necromanciarr/internal/doctor"
)

var statusMarks = map[doctor.Status]string{doctor.OK: "✓", doctor.Warn: "!", doctor.Fail: "✗", doctor.Skipped: "-"}

// doctorOptions sont les options propres à doctor ; les autres vont à config.Load.
type doctorOptions struct {
	jsonOutput bool
	checkIDs   []string
	configArgs []string
}

// splitDoctorArgs sépare --json et --check (répétable, « --check id » ou « --check=id ») des options de configuration.
func splitDoctorArgs(args []string) (doctorOptions, error) {
	var options doctorOptions
	for position := 0; position < len(args); position++ {
		argument := args[position]
		switch {
		case argument == "--json" || argument == "-json":
			options.jsonOutput = true
		case argument == "--check" || argument == "-check":
			position++
			if position >= len(args) {
				return doctorOptions{}, fmt.Errorf("%w: --check needs a value", config.ErrUsage)
			}
			options.checkIDs = append(options.checkIDs, args[position])
		case strings.HasPrefix(argument, "--check="):
			options.checkIDs = append(options.checkIDs, strings.TrimPrefix(argument, "--check="))
		default:
			options.configArgs = append(options.configArgs, argument)
		}
	}
	return options, nil
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	options, err := splitDoctorArgs(args)
	if err != nil {
		return reportError(stderr, err)
	}
	loadedConfig, err := config.Load(options.configArgs, os.LookupEnv, defaultConfigDir())
	if errors.Is(err, config.ErrUsage) {
		return reportError(stderr, err)
	}
	checks := configLoadFailure(err)
	if err == nil {
		checks = doctor.Build(loadedConfig, doctor.Deps{})
	}
	selected, err := selectChecks(checks, options.checkIDs)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return usageExitCode
	}
	report := doctor.Run(context.Background(), selected)
	return printReport(report, options.jsonOutput, stdout, stderr)
}

// configLoadFailure remplace tous les contrôles par un seul : sans configuration valide, les autres n'ont pas de sens.
func configLoadFailure(loadErr error) []doctor.Check {
	if loadErr == nil {
		return nil
	}
	return []doctor.Check{{ID: doctor.IDConfig, Title: "Configuration", Run: func(context.Context) doctor.Result {
		return doctor.Result{Status: doctor.Fail, Detail: loadErr.Error(), Hint: "fix the configuration, then run `necromanciarr config check`"}
	}}}
}

func selectChecks(checks []doctor.Check, wantedIDs []string) ([]doctor.Check, error) {
	if len(wantedIDs) == 0 {
		return checks, nil
	}
	var selected []doctor.Check
	for _, check := range checks {
		for _, wantedID := range wantedIDs {
			if check.ID == wantedID {
				selected = append(selected, check)
				break
			}
		}
	}
	for _, wantedID := range wantedIDs {
		if !hasCheck(checks, wantedID) {
			return nil, fmt.Errorf("unknown check %q (valid ids: %s)", wantedID, strings.Join(checkIDs(checks), ", "))
		}
	}
	return selected, nil
}

func hasCheck(checks []doctor.Check, id string) bool {
	for _, check := range checks {
		if check.ID == id {
			return true
		}
	}
	return false
}

func checkIDs(checks []doctor.Check) []string {
	ids := make([]string, 0, len(checks))
	for _, check := range checks {
		ids = append(ids, check.ID)
	}
	return ids
}

func printReport(report doctor.Report, jsonOutput bool, stdout, stderr io.Writer) int {
	if jsonOutput {
		rendered, err := doctor.MarshalJSONReport(report)
		if err != nil {
			return reportError(stderr, err)
		}
		_, _ = fmt.Fprintln(stdout, string(rendered))
		return doctor.ExitCode(report)
	}
	for _, named := range report.Checks {
		_, _ = fmt.Fprintf(stdout, "%s %s: %s\n", statusMarks[named.Status], named.Title, named.Detail)
		if named.Hint != "" {
			_, _ = fmt.Fprintf(stdout, "    hint: %s\n", named.Hint)
		}
	}
	return doctor.ExitCode(report)
}
