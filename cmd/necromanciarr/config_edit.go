package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
)

// Options des sous-commandes d'édition : la valeur ou l'élément est lu sur l'entrée standard, hors de l'historique du shell.
const (
	flagValueStdin    = "value-stdin"
	flagElementStdin  = "element-stdin"
	maxStdinBytes     = 1 << 20
	actionConfigGet   = "config get"
	actionConfigSet   = "config set"
	actionConfigUnset = "config unset"
	actionItemSet     = "config item-set"
	actionItemDelete  = "config item-delete"
	actionItemUnset   = "config item-unset"
	exampleRadarrURL  = "http://example.com:7878"
)

// stdin est l'entrée lue par --value-stdin et --element-stdin ; les tests la remplacent.
var stdin io.Reader = os.Stdin

func configCommands() []command {
	itemArguments := []string{app.ArgumentConfigList, app.ArgumentConfigItemID}
	return []command{
		{
			path: []string{"config", "get"}, summaryKey: i18n.KeyCLISummaryConfigGet, actions: []string{actionConfigGet},
			arguments: []string{app.ArgumentConfigKey}, optionalArguments: true, example: []string{}, execute: runConfigGet,
		},
		{
			path: []string{"config", "set"}, summaryKey: i18n.KeyCLISummaryConfigSet, actions: []string{actionConfigSet},
			arguments: []string{app.ArgumentConfigKey, app.ArgumentConfigValue}, optionalArguments: true,
			boolFlags: []string{flagValueStdin}, example: []string{"linkCategory", `"category-a"`}, execute: runConfigSet,
		},
		{
			path: []string{"config", "unset"}, summaryKey: i18n.KeyCLISummaryConfigUnset, actions: []string{actionConfigUnset},
			arguments: []string{app.ArgumentConfigKey}, example: []string{"linkCategory"}, execute: runConfigUnset,
		},
		{
			path: []string{"config", "item", "set"}, summaryKey: i18n.KeyCLISummaryConfigItemSet, actions: []string{actionItemSet},
			arguments: []string{app.ArgumentConfigList, app.ArgumentConfigItemID, app.ArgumentConfigElement}, optionalArguments: true,
			boolFlags: []string{flagElementStdin}, example: []string{"radarr", exampleRadarrURL, `{"url": "` + exampleRadarrURL + `"}`},
			execute: runConfigItemSet,
		},
		{
			path: []string{"config", "item", "delete"}, summaryKey: i18n.KeyCLISummaryConfigItemDelete, actions: []string{actionItemDelete},
			arguments: itemArguments, example: []string{"radarr", exampleRadarrURL},
			execute: itemMutation(actionItemDelete, app.Service.DeleteItem),
		},
		{
			path: []string{"config", "item", "unset"}, summaryKey: i18n.KeyCLISummaryConfigItemUnset, actions: []string{actionItemUnset},
			arguments: itemArguments, example: []string{"radarr", exampleRadarrURL},
			execute: itemMutation(actionItemUnset, app.Service.UnsetItem),
		},
	}
}

// configSaved est la sortie d'une écriture : elle ne s'applique qu'au prochain démarrage du serveur.
type configSaved struct {
	Action          string `json:"action"`
	RestartRequired bool   `json:"restartRequired"`
}

func saved(translator i18n.Translator, action string) output {
	result := configSaved{Action: action, RestartRequired: true}
	return output{jsonValue: result, text: func(writer io.Writer) {
		_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIConfigSaved, textParams("action", action)))
	}}
}

// runConfigGet rend toute la vue, ou le seul réglage ou la seule liste nommée ; les secrets y sont déjà nuls.
func runConfigGet(ctx context.Context, call *invocation) (output, error) {
	view, err := call.service.ConfigView(ctx)
	if err != nil {
		return output{}, err
	}
	key, hasKey := call.arguments[app.ArgumentConfigKey]
	if !hasKey {
		return indentedJSON(view), nil
	}
	if setting, isSetting := view.Settings[key]; isSetting {
		return indentedJSON(setting), nil
	}
	if items, isList := view.Lists[key]; isList {
		return indentedJSON(nonNil(items)), nil
	}
	return output{}, fmt.Errorf("%w: %q", app.ErrUnknownConfigKey, key)
}

func indentedJSON(value any) output {
	return output{jsonValue: value, text: func(writer io.Writer) {
		encoded, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return
		}
		_, _ = fmt.Fprintln(writer, string(encoded))
	}}
}

func runConfigSet(ctx context.Context, call *invocation) (output, error) {
	key, hasKey := call.arguments[app.ArgumentConfigKey]
	if !hasKey {
		return output{}, fmt.Errorf("%w: missing %s", config.ErrUsage, app.ArgumentConfigKey)
	}
	value, err := jsonInput(call, app.ArgumentConfigValue, flagValueStdin)
	if err != nil {
		return output{}, err
	}
	if !json.Valid(value) {
		return output{}, fmt.Errorf("%w: %s is not valid JSON", config.ErrUsage, app.ArgumentConfigValue)
	}
	if err := call.service.SetSetting(ctx, key, value); err != nil {
		return output{}, err
	}
	return saved(call.translator, actionConfigSet), nil
}

func runConfigUnset(ctx context.Context, call *invocation) (output, error) {
	if err := call.service.UnsetSetting(ctx, call.arguments[app.ArgumentConfigKey]); err != nil {
		return output{}, err
	}
	return saved(call.translator, actionConfigUnset), nil
}

func runConfigItemSet(ctx context.Context, call *invocation) (output, error) {
	itemID, hasItemID := call.arguments[app.ArgumentConfigItemID]
	if !hasItemID {
		return output{}, fmt.Errorf("%w: missing %s, %s", config.ErrUsage, app.ArgumentConfigList, app.ArgumentConfigItemID)
	}
	encoded, err := jsonInput(call, app.ArgumentConfigElement, flagElementStdin)
	if err != nil {
		return output{}, err
	}
	var element map[string]any
	if err := json.Unmarshal(encoded, &element); err != nil || element == nil {
		return output{}, fmt.Errorf("%w: %s must be a JSON object", config.ErrUsage, app.ArgumentConfigElement)
	}
	if err := call.service.PutItem(ctx, call.arguments[app.ArgumentConfigList], itemID, element); err != nil {
		return output{}, err
	}
	return saved(call.translator, actionItemSet), nil
}

// itemMethod est DeleteItem ou UnsetItem, appelée sur le Service de l'invocation.
type itemMethod func(service app.Service, ctx context.Context, list, itemID string) error

func itemMutation(action string, mutate itemMethod) func(ctx context.Context, call *invocation) (output, error) {
	return func(ctx context.Context, call *invocation) (output, error) {
		list, itemID := call.arguments[app.ArgumentConfigList], call.arguments[app.ArgumentConfigItemID]
		if err := mutate(call.service, ctx, list, itemID); err != nil {
			return output{}, err
		}
		return saved(call.translator, action), nil
	}
}

// jsonInput prend le JSON en argument ou sur l'entrée standard, jamais les deux ni aucun.
func jsonInput(call *invocation, argument, stdinFlag string) ([]byte, error) {
	positional, hasPositional := call.arguments[argument]
	readsStdin := call.has(stdinFlag)
	if hasPositional == readsStdin {
		return nil, fmt.Errorf("%w: give either %s or --%s", config.ErrUsage, argument, stdinFlag)
	}
	if hasPositional {
		return []byte(positional), nil
	}
	return readStdin(stdinFlag)
}

func readStdin(stdinFlag string) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(stdin, maxStdinBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: --%s: %w", config.ErrUsage, stdinFlag, err)
	}
	if len(content) > maxStdinBytes {
		return nil, fmt.Errorf("%w: --%s: more than %d bytes", config.ErrUsage, stdinFlag, maxStdinBytes)
	}
	return bytes.TrimSpace(content), nil
}
