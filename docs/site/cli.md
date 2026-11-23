# Ligne de commande

```text
necromanciarr <api-key|config check|serve|scan|match|verify|link|seed>
```

Une ligne de commande invalide affiche cet usage et sort avec le code 2 ; une erreur d'exécution sort avec le code 1.
Les options de [configuration](/configuration) (`--config-dir`, `--listen`, …) s'appliquent à toutes les commandes.

## `api-key`

Affiche le jeton de l'interface web stocké dans `<dossier de configuration>/api-key`.
S'il n'existe pas, il est créé (32 octets aléatoires en hexadécimal, fichier en `0600`) ;
un jeton existant n'est jamais écrasé.

```bash
necromanciarr api-key --config-dir /home/u/necromanciarr
```

## `config check`

Charge la configuration comme le ferait le service et affiche la configuration effective en JSON.
Les mots de passe et jetons sont masqués. Sort en erreur si la configuration est invalide.

```bash
necromanciarr config check --config-dir /home/u/necromanciarr
```

::: warning À venir
`serve`, `scan`, `match`, `verify`, `link` et `seed` figurent dans l'usage mais ne sont pas encore implémentées.
:::
