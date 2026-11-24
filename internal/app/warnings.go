package app

import (
	"slices"

	"necromanciarr/internal/i18n"
	"necromanciarr/internal/pipeline"
)

// Warning est un avertissement rendu pour l'opérateur : Code est la clé stable du message (jamais traduite), Text son
// rendu dans la langue configurée ; un avertissement hérité d'avant le catalogue porte le code legacy.text.
type Warning = i18n.Rendered

// maintenanceWarningKeys relie les codes d'avertissement de dedupe et repair à leur message.
var maintenanceWarningKeys = map[string]i18n.Key{
	WarningPausedIncompleteAfterRecheck: i18n.KeyWarningPausedIncompleteAfterRecheck,
	WarningRecheckFailed:                i18n.KeyWarningRecheckFailed,
	WarningNoClientRecheck:              i18n.KeyWarningNoClientRecheck,
	WarningLinkNotRecorded:              i18n.KeyWarningLinkNotRecorded,
	// Les avertissements du plan dedupe sont stockés sous leur clé.
	string(i18n.KeyWarningDedupeUnreliableInode): i18n.KeyWarningDedupeUnreliableInode,
	string(i18n.KeyWarningDedupeFullNetworkRead): i18n.KeyWarningDedupeFullNetworkRead,
	string(i18n.KeyWarningDedupeSampledVerify):   i18n.KeyWarningDedupeSampledVerify,
	string(i18n.KeyWarningDedupeRenameNotAtomic): i18n.KeyWarningDedupeRenameNotAtomic,
}

// messagesOfCodes convertit les avertissements de dedupe et repair en messages stockables : un code connu devient sa
// clé, tout autre texte (celui que les paquets dedupe et repair produisent encore) est gardé tel quel.
func messagesOfCodes(codes []string) []i18n.Message {
	messages := make([]i18n.Message, 0, len(codes))
	for _, code := range codes {
		if key, known := maintenanceWarningKeys[code]; known {
			messages = append(messages, i18n.Message{Key: key})
			continue
		}
		messages = append(messages, i18n.Message{Key: i18n.KeyLegacyText, Params: map[string]string{"text": code}})
	}
	return messages
}

// warningViews rend les messages stockés ; jamais nil, pour que le JSON porte toujours un tableau. Une ligne héritée
// qui contient un code de dedupe ou repair est rendue comme sa clé, et un même avertissement n'apparaît qu'une fois.
func warningViews(translator i18n.Translator, messages []i18n.Message) []Warning {
	views := make([]Warning, 0, len(messages))
	for _, message := range messages {
		view := translator.Rendered(message)
		if key, isCode := maintenanceKeyOfLegacy(message); isCode {
			view = translator.Rendered(i18n.Message{Key: key})
		}
		if !slices.Contains(views, view) {
			views = append(views, view)
		}
	}
	return views
}

// maintenanceKeyOfLegacy dit si un message hérité porte un code d'avertissement de dedupe ou repair.
func maintenanceKeyOfLegacy(message i18n.Message) (i18n.Key, bool) {
	if message.Key != i18n.KeyLegacyText {
		return "", false
	}
	key, known := maintenanceWarningKeys[message.Params["text"]]
	return key, known
}

// withRenderedTexts rend les libellés de motifs et les avertissements globaux d'un résultat d'étape.
func withRenderedTexts(translator i18n.Translator, result pipeline.Result) pipeline.Result {
	result = withReasonLabels(translator, result)
	for _, warning := range result.Warnings {
		result.WarningViews = append(result.WarningViews, translator.Rendered(warning))
	}
	return result
}

// withReasonLabels ajoute au résultat d'une étape le libellé traduit de chaque motif compté ; les codes restent la seule
// clé des dénombrements.
func withReasonLabels(translator i18n.Translator, result pipeline.Result) pipeline.Result {
	if len(result.Reasons) == 0 {
		return result
	}
	result.ReasonLabels = make(map[string]string, len(result.Reasons))
	for code := range result.Reasons {
		result.ReasonLabels[code] = translator.ReasonLabel(code)
	}
	return result
}
