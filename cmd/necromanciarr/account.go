package main

import (
	"context"
	"fmt"
	"io"

	"necromanciarr/internal/i18n"
)

// accountCommands : recours en CLI seule, hors du registre d'actions et sans route web, car ils servent
// quand l'accès web est perdu. Ils ouvrent la base sans amorcer les secrets et tolèrent une configuration
// stockée invalide.
var accountCommands = buildAccountCommands()

func buildAccountCommands() []command {
	return []command{
		{
			path: []string{"account", "status"}, summaryKey: i18n.KeyCLIUsageAccountStatus,
			boolFlags: []string{flagJSON}, repairsStoredConfig: true, execute: runAccountStatus,
		},
		{
			path: []string{"account", "reset"}, summaryKey: i18n.KeyCLIUsageAccountReset,
			boolFlags: []string{flagYes, flagJSON}, repairsStoredConfig: true, execute: runAccountReset,
		},
	}
}

// accountStatusView est la sortie stable de account status : ni empreinte, ni secret, ni jeton.
type accountStatusView struct {
	Exists           bool   `json:"exists"`
	Username         string `json:"username"`
	SecondFactorKeys int    `json:"secondFactorKeys"`
	Passkeys         int    `json:"passkeys"`
	TOTP             bool   `json:"totp"`
}

type accountResetView struct {
	Removed              bool `json:"removed"`
	ConfirmationRequired bool `json:"confirmationRequired"`
}

func runAccountStatus(ctx context.Context, call *invocation) (output, error) {
	summary, err := call.service.AccountSummary(ctx)
	if err != nil {
		return output{}, err
	}
	view := accountStatusView{
		Exists: summary.Exists, Username: summary.Username, SecondFactorKeys: summary.SecondFactorKeys,
		Passkeys: summary.Passkeys, TOTP: summary.TOTP,
	}
	return output{jsonValue: view, text: func(writer io.Writer) { writeAccountStatus(writer, call.translator, view) }}, nil
}

func writeAccountStatus(writer io.Writer, translator i18n.Translator, view accountStatusView) {
	if !view.Exists {
		_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIAccountNone, nil))
		return
	}
	totpKey := i18n.KeyCLIAccountTOTPNotSet
	if view.TOTP {
		totpKey = i18n.KeyCLIAccountTOTPSet
	}
	_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIAccountUsername, textParams("username", view.Username)))
	_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIAccountSecondFactorKeys, textParams("count", fmt.Sprint(view.SecondFactorKeys))))
	_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIAccountPasskeys, textParams("count", fmt.Sprint(view.Passkeys))))
	_, _ = fmt.Fprintln(writer, translator.Text(totpKey, nil))
}

// runAccountReset n'efface rien sans --yes : il explique ce qui serait supprimé et sort avec le code d'usage.
func runAccountReset(ctx context.Context, call *invocation) (output, error) {
	if !call.has(flagYes) {
		return accountMessage(call, accountResetView{ConfirmationRequired: true}, i18n.KeyCLIAccountResetNeedsYes, usageExitCode), nil
	}
	if err := call.service.ResetAccount(ctx); err != nil {
		return output{}, err
	}
	return accountMessage(call, accountResetView{Removed: true}, i18n.KeyCLIAccountResetDone, successExitCode), nil
}

func accountMessage(call *invocation, view accountResetView, key i18n.Key, exitCode int) output {
	return output{jsonValue: view, exitCode: exitCode, text: func(writer io.Writer) {
		_, _ = fmt.Fprintln(writer, call.translator.Text(key, nil))
	}}
}
