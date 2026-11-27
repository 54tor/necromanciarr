# Installation

Necromanciarr se distribue sous forme d'une image Docker construite depuis le dépôt.
Aucune image publique n'est encore publiée.

## Construire l'image

```bash
make docker-build            # produit necromanciarr:dev
```

L'image repose sur `debian:trixie-slim`, avec un binaire statique (`CGO_ENABLED=0`).

## Lancer un conteneur

```bash
docker run --rm \
  --user 1000:1000 \
  -v /home/u/necromanciarr:/data \
  necromanciarr:dev config check
```

| Élément | Valeur |
| --- | --- |
| Volume | `/data` : contient `.env` et la base SQLite, qui porte toute la configuration |
| Variable `CONFIG_DIR` | `/data` par défaut dans l'image |
| Utilisateur | `1000:1000` par défaut ; à aligner sur les droits de la bibliothèque avec `--user` |
| Commande par défaut | `serve` (voir [CLI](/cli) : pas encore disponible) |

::: warning À venir
Le service (`serve`) n'est pas encore implémenté : aujourd'hui, l'image permet d'exécuter
`config check` et `api-key`. Les images multi-architecture (arm64, riscv64) ne sont pas encore construites.
:::

## Première configuration

1. Créer un dossier de configuration inscriptible : au premier `serve`, `NECROMANCIARR_SECRET_KEY` et `NECROMANCIARR_AUTH_TOKEN` y sont générés dans le `.env` (voir [Configuration](/configuration#premier-demarrage)). Pour les fournir soi-même, `necromanciarr api-key --secret` tire la clé.
2. Enregistrer les réglages par la page Configuration ou par `necromanciarr config set` et `config item set` (voir [Configuration](/configuration)).
3. Vérifier avec `config check`, qui affiche la configuration effective, secrets masqués, avec l'origine de chaque valeur.
