package i18n

// Clés de l'édition de la configuration : actions config get/set/unset/item-*, sous-actions et erreurs.
const (
	KeyActionConfigGet        Key = "action.config.get"
	KeyActionConfigSet        Key = "action.config.set"
	KeyActionConfigUnset      Key = "action.config.unset"
	KeyActionConfigItemSet    Key = "action.config.item_set"
	KeyActionConfigItemDelete Key = "action.config.item_delete"
	KeyActionConfigItemUnset  Key = "action.config.item_unset"

	KeyCLISummaryConfigGet        Key = "cli.summary.config_get"
	KeyCLISummaryConfigSet        Key = "cli.summary.config_set"
	KeyCLISummaryConfigUnset      Key = "cli.summary.config_unset"
	KeyCLISummaryConfigItemSet    Key = "cli.summary.config_item_set"
	KeyCLISummaryConfigItemDelete Key = "cli.summary.config_item_delete"
	KeyCLISummaryConfigItemUnset  Key = "cli.summary.config_item_unset"
	KeyCLIConfigSaved             Key = "cli.config.saved"

	KeyErrorSecretKeyMissing    Key = "error.secret_key_missing"
	KeyErrorInvalidStoredConfig Key = "error.invalid_stored_config"
	KeyErrorUnknownConfigKey    Key = "error.unknown_config_key"
	KeyErrorWebFieldRequired    Key = "error.web.field_required"
)

var configKeys = []Key{
	KeyActionConfigGet, KeyActionConfigSet, KeyActionConfigUnset, KeyActionConfigItemSet, KeyActionConfigItemDelete,
	KeyActionConfigItemUnset, KeyCLISummaryConfigGet, KeyCLISummaryConfigSet, KeyCLISummaryConfigUnset,
	KeyCLISummaryConfigItemSet, KeyCLISummaryConfigItemDelete, KeyCLISummaryConfigItemUnset, KeyCLIConfigSaved,
	KeyErrorSecretKeyMissing, KeyErrorInvalidStoredConfig, KeyErrorUnknownConfigKey, KeyErrorWebFieldRequired,
}
