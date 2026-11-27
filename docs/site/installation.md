# Installation

Necromanciarr se distribue sous forme d'une image Docker : construite depuis le dépôt, ou publiée en instantané sur le registre GitHub à chaque push sur `main`.

## Obtenir l'image

```bash
make docker-build            # construit necromanciarr:dev (interface web et binaire statique, debian:trixie-slim)
```

Instantané publié : `ghcr.io/<propriétaire>/necromanciarr:snapshot` (dernier `main`) et `…:sha-<commit court>` (un commit précis), pour `linux/amd64` et `linux/arm64`. Il n'est pas versionné : en production, épingler le tag `sha-…`. Le binaire `linux/riscv64` est construit mais jamais exécuté en CI.

## Lancer avec Docker Compose

Le dépôt fournit `compose.example.yaml` :

```bash
cp compose.example.yaml compose.yaml
printf 'PUID=%s\nPGID=%s\n' "$(id -u)" "$(id -g)" > .env   # propriétaire de la bibliothèque
mkdir data                                                  # doit appartenir à PUID:PGID
$EDITOR compose.yaml                                        # /srv/storage : dossier qui contient bibliothèque ET liens
docker compose up -d
docker compose logs necromanciarr                           # le jeton d'accès s'affiche ici, une seule fois
```

Ouvrir `http://127.0.0.1:8787`, saisir le jeton : l'assistant de configuration prend la suite (voir [Configuration](/configuration#premier-demarrage)). Une fois les réglages enregistrés, `docker restart necromanciarr` les applique.

| Élément | Valeur |
| --- | --- |
| Volume `/data` | `.env` et base SQLite, qui porte toute la configuration. Un dossier de l'hôte doit appartenir à `PUID:PGID` ; un volume nommé neuf garde le propriétaire `1000:1000` de l'image |
| Utilisateur | `PUID:PGID` du compose (`1000:1000` par défaut dans l'image), à aligner sur les droits de la bibliothèque |
| Bibliothèque et liens | **un seul montage** (`/media` dans l'exemple) : un hardlink entre deux montages échoue avec `EXDEV` |
| Port | `8787`, publié sur `127.0.0.1` dans l'exemple |
| Commande par défaut | `serve` ; toute autre sous-commande la remplace (`docker compose run --rm necromanciarr doctor`) |

L'image écoute sur toutes les interfaces : `serve` exige donc un jeton, qu'il génère au premier démarrage dans `/data/.env`.

## Sans Docker

```bash
make ui && make build        # interface embarquée, puis ./necromanciarr
./necromanciarr serve        # http://127.0.0.1:8787
```

Le dossier de configuration est `--config-dir`, `CONFIG_DIR`, ou celui de l'utilisateur ; il est créé s'il s'agit du dossier par défaut.

## Vérifier

`necromanciarr doctor` contrôle la configuration, la base, les services, les volumes et les dossiers de liens ; la page Démarrage de l'interface affiche les mêmes contrôles.
