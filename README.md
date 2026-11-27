# Necromanciarr

Necromanciarr retrouve, chez un fournisseur de bibliothèque, les torrents dont la médiathèque possède déjà les fichiers (identifiés par Radarr et Sonarr), prouve chaque fichier par les SHA-1 des pièces, puis reconstitue le torrent par des liens dans un dossier à part pour le remettre en partage dans qBittorrent. Il sait aussi remplacer les copies côté torrent par des liens (dedupe) et réparer des liens cassés (repair).

## Ce qu'il ne fait jamais

- Il n'écrit jamais dans la bibliothèque : ni écriture, ni déplacement, ni suppression d'un fichier média. Liens, annexes et copies vont dans les dossiers de liens, qui ne peuvent pas recouvrir une racine de bibliothèque.
- Il n'agit jamais sans proposition acceptée : chaque lien, chaque ajout au client, chaque déduplication ou réparation part d'une proposition que l'opérateur accepte (CLI ou interface web).
- Un torrent incomplet n'est jamais repris d'office : il reste en pause jusqu'à un `force` explicite, qui isole d'abord les médias exposés par une copie indépendante.

## Démarrage rapide

```sh
make ui && make build          # interface web embarquée, puis binaire ./necromanciarr
./necromanciarr serve          # API et interface sur http://127.0.0.1:8787
```

Au premier `serve`, la clé de scellement et le jeton d'accès sont générés dans le `.env` du dossier de configuration ; le jeton est affiché une seule fois sur stderr. Ouvrir l'URL, coller le jeton, puis suivre l'assistant (onglet Assistant) : il teste Radarr, Sonarr et qBittorrent avant d'enregistrer, et indique le redémarrage final. `./necromanciarr doctor` vérifie ensuite configuration, base, services et dossiers de liens. Chemin manuel : définir `NECROMANCIARR_SECRET_KEY` et `NECROMANCIARR_AUTH_TOKEN` soi-même (`./necromanciarr api-key` tire un jeton, `api-key --secret` une clé ; rien n'est écrit).

La configuration vit dans la base SQLite du dossier de configuration (`--config-dir`, `CONFIG_DIR`, ou `~/.config/necromanciarr`) ; elle se modifie par la page Configuration de l'interface ou par `necromanciarr config set` et `config item set`, et le `config.js` de cross-seed (`CROSS_SEED_CONFIG`) en fournit les valeurs par défaut, surchargeables élément par élément. Le `.env` du même dossier ne porte que l'amorçage et les jetons (modèle : [`.env.example`](.env.example)), dont `NECROMANCIARR_SECRET_KEY`, qui scelle les secrets en base. Pour seeder les torrents du fournisseur, qui n'ont pas d'annonce, la liste `trackers` est indispensable (voir [`docs/reference/02-configuration.md`](docs/reference/02-configuration.md)). Le pipeline se lance étape par étape : `scan`, `match`, `verify`, puis `proposals list` et `proposals accept <id>`, puis `link` et `seed`.

`language` (`fr` ou `en`) fixe la langue de la CLI, de l'API et de l'interface web. Les indexeurs Torznab et Prowlarr (`torznab`, `prowlarr`, `sourceOrder`) complètent le fournisseur pour les fichiers qu'il ne couvre pas. Une bibliothèque ou un dossier de liens peut vivre sur un hôte distant (volume `sftp://`, voir [`docs/reference/05-docker-and-volumes.md`](docs/reference/05-docker-and-volumes.md)).

## Docker

```sh
make docker-build                                   # image necromanciarr:dev
docker run -d --name necromanciarr -p 8787:8787 \
  -v necromanciarr-data:/data \
  -v /srv/storage:/media \
  necromanciarr:dev
docker logs necromanciarr                           # le jeton y figure, et dans /data/.env
```

L'image écoute sur `0.0.0.0:8787` : au premier démarrage `serve` génère le jeton dans `/data/.env` (un `NECROMANCIARR_AUTH_TOKEN` d'au moins 32 caractères fourni par `-e` l'emporte) ; les commandes ponctuelles (`config check`, `doctor`) n'en ont pas besoin. `/data` porte `.env` et la base, qui contient la configuration. La bibliothèque et les dossiers de liens doivent être sous **un seul montage commun** (ici `/media`), sinon un hardlink échoue avec `EXDEV`.

## Documentation

- Référence complète : [`docs/reference/00-index.md`](docs/reference/00-index.md) (pipeline, configuration, sécurité du seed, CLI et web, Docker, limites).
- Licences des dépendances liées dans le binaire et l'interface : [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) (régénéré par `make notices`, livré dans l'image sous `/usr/share/doc/necromanciarr/`).
- Contrat HTTP du fournisseur de bibliothèque : [`docs/contract/openapi.yaml`](docs/contract/openapi.yaml).

## Plateformes

| Plateforme | État |
|---|---|
| Linux `amd64` | binaire et image construits, tests exécutés en CI |
| Linux `arm64`, `riscv64` | binaires et images construits en CI, **jamais exécutés** (pas d'émulation) |
| Windows `amd64`, `arm64` | binaires construits ; tests des paquets `linker`, `config`, `pipeline` et `fsys` exécutés en CI sur `amd64` |
