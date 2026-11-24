package i18n

var french = map[Key]string{
	KeyLegacyText:                "{text}",
	KeyErrorUsageUnknownLanguage: "langue inconnue « {value} » : valeurs admises fr, en",
	KeyErrorInternal:             "erreur interne (détail dans le journal du serveur)",
	KeyConfigCheckLanguage:       "Langue : {language} (source : {source})",
}
