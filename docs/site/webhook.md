# Webhook

::: warning À venir
Le webhook n'est pas encore implémenté. Cette page décrit l'intention actuelle.
:::

Le service exposera `POST /api/webhook` pour être notifié par Sonarr, Radarr ou un outil tiers
qu'un média vient d'arriver, avec la clé d'API de l'interface dans l'en-tête `X-Api-Key`
(clé obtenue par [`api-key`](/cli#api-key)).
