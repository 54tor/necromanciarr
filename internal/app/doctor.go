package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"necromanciarr/internal/doctor"
)

// ErrUnknownCheck : un identifiant de contrôle demandé n'existe pas ; le message liste les identifiants valides.
var ErrUnknownCheck = errors.New("app: unknown check")

// DoctorOptions restreint le diagnostic : CheckIDs vide lance tous les contrôles ; Timeout 0 vaut doctor.DefaultTimeout
// par contrôle.
type DoctorOptions struct {
	CheckIDs []string      `json:"check_ids,omitempty"`
	Timeout  time.Duration `json:"timeout,omitempty"`
}

func (svc *service) Doctor(ctx context.Context, options DoctorOptions) (doctor.Report, error) {
	if registry := svc.deps.DoctorDeps.Registry; registry != nil {
		registry.ResetForStage() // un volume coupé par une étape précédente retente une fois pour le diagnostic
	}
	checks := doctor.Build(svc.deps.Config, svc.deps.DoctorDeps)
	selected, err := selectChecks(checks, options.CheckIDs)
	if err != nil {
		return doctor.Report{}, err
	}
	var runOptions []doctor.Option
	if options.Timeout > 0 {
		runOptions = append(runOptions, doctor.WithTimeout(options.Timeout))
	}
	return doctor.Run(ctx, selected, runOptions...), nil
}

// selectChecks garde l'ordre de Build ; un identifiant inconnu refuse tout plutôt que d'en ignorer un.
func selectChecks(checks []doctor.Check, wantedIDs []string) ([]doctor.Check, error) {
	if len(wantedIDs) == 0 {
		return checks, nil
	}
	knownIDs := make([]string, 0, len(checks))
	for _, check := range checks {
		knownIDs = append(knownIDs, check.ID)
	}
	for _, wantedID := range wantedIDs {
		if !slices.Contains(knownIDs, wantedID) {
			return nil, fmt.Errorf("%w %q (valid ids: %s)", ErrUnknownCheck, wantedID, strings.Join(knownIDs, ", "))
		}
	}
	var selected []doctor.Check
	for _, check := range checks {
		if slices.Contains(wantedIDs, check.ID) {
			selected = append(selected, check)
		}
	}
	return selected, nil
}
