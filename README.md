# Necromanciarr

Necromanciarr retrouve, chez un fournisseur de bibliothèque, les torrents dont la médiathèque possède déjà les fichiers (identifiés par Radarr et Sonarr), prouve chaque fichier par les SHA-1 des pièces, puis reconstitue le torrent par des liens dans un dossier à part pour le remettre en partage dans qBittorrent. Il sait aussi remplacer les copies côté torrent par des liens (dedupe) et réparer des liens cassés (repair).

## Ce qu'il ne fait jamais

- Il n'écrit jamais dans la bibliothèque : ni écriture, ni déplacement, ni suppression d'un fichier média. Liens, annexes et copies vont dans les dossiers de liens, qui ne peuvent pas recouvrir une racine de bibliothèque.
- Il n'agit jamais sans proposition acceptée : chaque lien, chaque ajout au client, chaque déduplication ou réparation part d'une proposition que l'opérateur accepte (CLI ou interface web).
- Un torrent incomplet n'est jamais repris d'office : il reste en pause jusqu'à un `force` explicite, qui isole d'abord les médias exposés par une copie indépendante.

## Démarrage rapide

```sh
make ui && make build          # interface web embarquée, puis binaire ./necromanciarr
./necromanciarr doctor         # vérifie configuration, base, services et dossiers de liens
./necromanciarr api-key        # tire un jeton pour NECROMANCIARR_AUTH_TOKEN (affiché, jamais écrit)
./necromanciarr serve          # API et interface sur http://127.0.0.1:8787
```

La configuration tient dans un dossier (`--config-dir`, `CONFIG_DIR`, ou `~/.config/necromanciarr`) qui contient `config.json` (modèle : [`config.example.json`](config.example.json)) et `.env` (modèle : [`.env.example`](.env.example)). Le pipeline se lance étape par étape : `scan`, `match`, `verify`, puis `proposals list` et `proposals accept <id>`, puis `link` et `seed`.

## Docker

```sh
make docker-build                                   # image necromanciarr:dev
docker run --rm necromanciarr:dev api-key           # jeton pour NECROMANCIARR_AUTH_TOKEN
docker run -d -p 8787:8787 \
  -e NECROMANCIARR_AUTH_TOKEN=<jeton> \
  -v necromanciarr-data:/data \
  -v /srv/storage:/media \
  necromanciarr:dev
```

L'image écoute sur `0.0.0.0:8787` : `serve` refuse de démarrer sans jeton d'au moins 32 caractères ; les commandes ponctuelles (`config check`, `doctor`) n'en ont pas besoin. `/data` porte `config.json`, `.env` et la base. La bibliothèque et les dossiers de liens doivent être sous **un seul montage commun** (ici `/media`), sinon un hardlink échoue avec `EXDEV`.

## Documentation

- Référence complète : [`docs/reference/00-index.md`](docs/reference/00-index.md) (pipeline, configuration, sécurité du seed, CLI et web, Docker, limites).
- Contrat HTTP du fournisseur de bibliothèque : [`docs/contract/openapi.yaml`](docs/contract/openapi.yaml).

## Plateformes

| Plateforme | État |
|---|---|
| Linux `amd64` | binaire et image construits, tests exécutés en CI |
| Linux `arm64`, `riscv64` | binaires et images construits en CI, **jamais exécutés** (pas d'émulation) |
| Windows `amd64`, `arm64` | binaires construits ; tests des paquets `linker`, `config` et `pipeline` exécutés en CI sur `amd64` |
