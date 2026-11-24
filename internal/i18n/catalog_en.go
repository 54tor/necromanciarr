package i18n

var english = map[Key]string{
	KeyLegacyText:                "{text}",
	KeyErrorUsageUnknownLanguage: `unknown language "{value}": allowed values are fr, en`,
	KeyErrorInternal:             "internal error (details in the server log)",
	KeyConfigCheckLanguage:       "Language: {language} (source: {source})",
}
