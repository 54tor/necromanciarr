# Docker et volumes

## L'image

Construite par `make docker-build` (étape Node pour l'interface, étape Go statique, étape finale `debian:trixie-slim` sans instruction `RUN`). `make docker-buildx` produit une archive OCI pour `linux/amd64`, `linux/arm64` et `linux/riscv64` sans pousser ni émuler ; `make cross-build` produit les binaires de ces trois cibles Linux et de `windows/amd64`, `windows/arm64`.

| Réglage de l'image | Valeur |
|---|---|
| Utilisateur | `1000:1000`, non root |
| Point d'entrée / commande | `necromanciarr` / `serve` : tout argument remplace `serve` |
| Variables | `CONFIG_DIR=/data`, `NECROMANCIARR_LISTEN=0.0.0.0:8787` |
| Port | `EXPOSE 8787` |
| Dossier de travail et volume | `/data`, propriété de `1000:1000` dans l'image |
| Sonde de santé | `necromanciarr healthcheck` toutes les 30 s, délai 5 s, démarrage 20 s, 3 essais |
| Contenu | base `debian:trixie-slim` (tag daté), le binaire et les certificats TLS ; fuseaux horaires embarqués dans le binaire |

L'image écoute sur toutes les interfaces, donc **`NECROMANCIARR_AUTH_TOKEN` est obligatoire pour `serve`** (la commande par défaut) : sans lui le conteneur sort en code 1 avec `listen "0.0.0.0:8787" is not loopback: set NECROMANCIARR_AUTH_TOKEN (at least 32 characters, generate one with necromanciarr api-key)`. Générer un jeton : `docker run --rm necromanciarr:dev api-key`.

## Volumes

| Montage | Mode | Rôle |
|---|---|---|
| `/data` | lecture-écriture, volume nommé | `config.json`, `.env`, `necromanciarr.db` (`dbPath` relatif, résolu depuis `CONFIG_DIR`) |
| bibliothèque **et** dossiers de liens | lecture-écriture, **un seul montage** | fichiers média et dossiers de `linkDirs` / `dedupe.torrentRoots` |
| dossier de cross-seed | **lecture seule**, dossier entier | `config.js` |

### Un seul montage pour la bibliothèque et les liens

Un hardlink ou un reflink entre deux montages distincts échoue avec `EXDEV`, même quand ils pointent le même disque de l'hôte : le noyau compare les montages, pas les disques. Monter le parent commun une seule fois et placer racines et dossiers de liens dessous :

```
/srv/storage            (hôte)  ->  /media  (conteneur, un seul montage)
  library/movies                      libraryRoots[0]  = /media/library/movies
  library/tv                          libraryRoots[1]  = /media/library/tv
  links                               linkDirs[0]      = /media/links
```

`doctor` (`link-feasibility`) refuse en `fail` un hardlink ou un reflink entre volumes différents et suggère « one bind mount covering both ». Le montage est en écriture parce que les liens y sont créés ; qu'aucun fichier de la bibliothèque ne soit écrit tient aux invariants du code ([03-seed-safety.md](03-seed-safety.md)), pas au montage. Un dossier de liens ne peut pas être dans une racine de bibliothèque : le placer à côté, sous le même parent.

Le client torrent doit lire le dossier du torrent. Il le voit sous son propre chemin : déclarer la règle `pathMappings` `{"from": "/media/links", "to": "<path seen by the client>"}`. Avec `linkType: symlink`, la cible du lien est un chemin absolu **de ce conteneur** (`/media/library/…`) : le client doit alors voir la bibliothèque au même chemin, sinon le lien est cassé de son côté. Avec un hardlink, la question ne se pose pas.

Dossier de liens distant (`sftp://<provider>/<absolute path>`) : les liens sont créés sur l'hôte distant, et le client torrent lit ce dossier soit sur cet hôte (seedbox), soit par un montage du même stockage. La règle traduit l'adresse distante vers la vue du client : `{"from": "sftp://seedbox/srv/links", "to": "/downloads/links"}`. Le côté `from` se compare au format POSIX et en respectant la casse, et ne couvre que les adresses du même fournisseur ; le côté `to` reste le chemin vu par le client, jamais une adresse `sftp://` (refus au chargement). Sans règle applicable, Seed refuse (`client_path_unmapped`) au lieu de supposer le même chemin.

### `config.js` de cross-seed

Monter le **dossier** qui contient `config.js`, en lecture seule, jamais le fichier seul : un montage de fichier unique se casse quand un éditeur remplace le fichier (nouvel inode, le conteneur garde l'ancien). Puis `CROSS_SEED_CONFIG=/cross-seed/config.js` (ou `crossSeedConfig`).

Chaque `process.env.NAME` que ce `config.js` référence doit aussi être fourni au conteneur Necromanciarr (variable d'environnement ou ligne de `/data/.env`) : le fichier est évalué ici, avec ces valeurs, pas avec l'environnement du conteneur de cross-seed. Le `localhost` du `config.js` désigne le conteneur de cross-seed : renseigner `crossSeed` (`http://cross-seed:2468?apikey=${CROSS_SEED_API_KEY}`) pour joindre le démon.

### `/data`

Nom de volume plutôt que dossier de l'hôte : un volume nommé neuf reprend le propriétaire `1000:1000` de l'image. Un dossier de l'hôte doit appartenir à l'utilisateur du conteneur. Pour y déposer `config.json` et `.env` :

```
docker run --rm --user 1000:1000 -v necromanciarr-data:/data -v "$PWD":/src:ro \
  --entrypoint cp necromanciarr:dev /src/config.json /src/.env /data/
```

### Droits

Le conteneur tourne en `1000:1000`. Pour une bibliothèque qui appartient à un autre compte, surcharger `user: "<uid>:<gid>"` avec celui qui peut lire la bibliothèque, écrire dans les dossiers de liens et dans `/data`. Le client torrent doit lire les fichiers sources par les liens : les droits de la bibliothèque s'appliquent à lui aussi (un hardlink garde le propriétaire et les droits du fichier). Les dossiers de torrent sont créés en `0755`.

## Exemple

```yaml
services:
  necromanciarr:
    image: necromanciarr:dev
    user: "1000:1000"
    restart: unless-stopped
    ports:
      - "127.0.0.1:8787:8787"
    environment:
      NECROMANCIARR_AUTH_TOKEN: ${NECROMANCIARR_AUTH_TOKEN:?générer avec necromanciarr api-key}
      CROSS_SEED_CONFIG: /cross-seed/config.js
      # process.env.* référencés par le config.js de cross-seed
      RADARR_API_KEY: ${RADARR_API_KEY}
      SONARR_API_KEY: ${SONARR_API_KEY}
      QBIT_PASSWORD: ${QBIT_PASSWORD}
    volumes:
      - necromanciarr-data:/data
      - /srv/storage:/media
      - /srv/appdata/cross-seed:/cross-seed:ro
    cpus: 2
    mem_limit: 2g
volumes:
  necromanciarr-data:
```

`/data/config.json` correspondant :

```json
{
  "libraryApi": "https://library.example.com",
  "libraryRoots": ["/media/library/movies", "/media/library/tv"],
  "linkDirs": ["/media/links"],
  "pathMappings": [{ "from": "/media/links", "to": "/downloads/links" }],
  "radarr": ["http://radarr:7878?apikey=${RADARR_API_KEY}"],
  "sonarr": ["http://sonarr:8989?apikey=${SONARR_API_KEY}"],
  "torrentClients": ["qbittorrent:http://alice:${QBIT_PASSWORD}@qbittorrent:8080"],
  "crossSeed": "http://cross-seed:2468?apikey=${CROSS_SEED_API_KEY}"
}
```

`LIBRARY_PROVIDER_TOKEN` et `CROSS_SEED_API_KEY` vont dans `/data/.env`. Le port n'est publié que sur la boucle locale de l'hôte : l'accès distant passe par un mandataire inverse (qui garde le jeton `Bearer`) ou un tunnel.

## Lancer une commande dans le conteneur

Le point d'entrée est le binaire : l'argument est la sous-commande.

```
docker compose exec necromanciarr necromanciarr verify
docker run --rm --user 1000:1000 -v necromanciarr-data:/data -v /srv/storage:/media \
  necromanciarr:dev doctor --json
```

Seul `serve` exige `NECROMANCIARR_AUTH_TOKEN` hors boucle locale : une commande ponctuelle (`config check`, `doctor`, `scan`…) et `healthcheck` tournent sans jeton malgré l'écoute `0.0.0.0:8787` de l'image. Un jeton fourni doit toujours faire 32 caractères au moins, quelle que soit la commande. Sans jeton, `doctor` signale l'écoute publique par son contrôle `auth` (`fail`).

Une commande lancée dans un second processus partage la base et le verrou d'étape avec le serveur : si une étape tourne déjà, elle échoue avec `stage_running` (exit 1) au lieu d'entrer en concurrence. Un `docker run` jetable doit monter les mêmes volumes que le service, sinon il voit une base et des chemins vides.

## Montage rclone

Une bibliothèque sur un montage rclone se lit comme un dossier local. Notes pour le montage de l'hôte :

- Monter avec `--vfs-links` pour que les symlinks aient une cible ; sans lui `doctor` signale « les symlinks échouent sur ce montage : relancer rclone avec --vfs-links ». Un montage rclone n'accepte en pratique ni hardlink ni reflink (la sonde de `doctor` tranche) : les dossiers de liens ne sont pas sur ce montage (voir [06-limits-and-troubleshooting.md](06-limits-and-troubleshooting.md)).
- Si rclone est relancé sur l'hôte, le conteneur ne voit le nouveau montage que si le montage de liaison propage les montages (`bind.propagation: rslave` dans Compose) ; sinon redémarrer le conteneur.
- Lancer le processus qui porte le montage avec `--rc --rc-addr :5572 --rc-user <utilisateur> --rc-pass <motdepasse>` et déclarer `fsProviders[].rc` vers `rclone:5572` sur un réseau privé : sans rc, aucune empreinte n'est disponible et la déduplication relit les fichiers.
- Le cache VFS de rclone peut faire croire à une date de modification restituée par le stockage : `doctor` l'écrit en conseil.

## Fournisseur SFTP

Un fournisseur `fsProviders` de type `sftp` ([02-configuration.md](02-configuration.md)) n'a besoin d'aucun montage pour le stockage distant : seuls ses fichiers de confiance et de clé entrent dans le conteneur.

- Monter `known_hosts` et la clé privée en lecture seule, par exemple `- /home/u/necromanciarr/ssh:/data/ssh:ro`, et déclarer `knownHosts: "/data/ssh/known_hosts"`, `identityFile: "/data/ssh/id_ed25519"` (chemins vus par le conteneur). La clé doit être lisible par l'utilisateur `1000:1000` et rester en `0600`.
- Remplir `known_hosts` sans `ssh-keyscan` : `docker compose run --rm necromanciarr sftp trust seedbox` affiche l'empreinte de la clé d'hôte sans rien écrire ni s'authentifier ; après l'avoir comparée à celle du serveur, relancer avec `--yes`. L'ajout demande ce dossier monté sans `:ro` et `known_hosts` modifiable par `1000:1000` le temps de la commande ; remettre ensuite `:ro`. Jamais depuis l'interface web ([04-cli-and-web.md](04-cli-and-web.md)).
- La passphrase de la clé ou le mot de passe passent par le `.env` de `/data` (`SFTP_KEY_PASSPHRASE=…`, référencé par `"${SFTP_KEY_PASSPHRASE}"`), jamais en clair dans `config.json`.
- L'agent SSH de l'hôte (`SSH_AUTH_SOCK`) n'est pas transmis au conteneur : `agent: true` exige de monter soi-même la socket et de définir `SSH_AUTH_SOCK`, ce que l'image ne fait pas. Préférer `identityFile`.
- Recommandé : un compte SFTP sans droit d'écriture sur la bibliothèque (ACL, groupe ou montage en lecture seule côté serveur), qui n'écrit que dans le dossier de liens, en défense en profondeur : sans ouverture relative à un dossier en SFTP, le confinement des écritures repose sur des contrôles de chemin (« Écriture distante » dans [02-configuration.md](02-configuration.md)). Ce n'est pas exigé ; un contrôle de `doctor` le signalera.
- La base (`dbPath`) reste dans `/data`, jamais sur le serveur SFTP.
- Serveurs SFTP Windows : non pris en charge.
- `necromanciarr doctor` vérifie chaque fournisseur : `sftp:<name>` (connexion, clé d'hôte, serveur Windows refusé), `sftp-library:<name>` (racine lisible, média dans les 2000 premières entrées, avertissement `library_maybe_writable` quand les modes et propriétaires laissent croire que le compte écrit, jamais par un essai d'écriture) et `sftp-exec:<name>` (commandes distantes), puis `link-feasibility` et `path-mappings` pour les dossiers de liens distants ([06-limits-and-troubleshooting.md](06-limits-and-troubleshooting.md)).

### Compte restreint et commandes distantes

`exec` (`auto` par défaut, `off`, `required`) règle les commandes distantes en lecture seule (`stat`, `sha1sum`, `md5sum`) que l'outil envoie par `ssh exec` sur la même connexion ; jamais d'écriture ni de lien par exec.

- `auto` : à la première racine d'écriture d'une session (le dossier de liens), la sonde vérifie le shell de connexion et les programmes, puis compare `stat` à SFTP sur ses propres fichiers de sonde (taille, date à une seconde près, inode non nul, un lien dur vu comme un seul fichier d'au moins deux liens, le dossier d'un autre inode) et `sha1sum`, `md5sum` sur l'octet qu'elle vient d'écrire. La sonde ne lit ni ne touche aucun chemin de la bibliothèque. Tout concorde : identité de fichier distante (`RemoteStat`) et empreintes calculées par l'hôte (`RemoteHash`). Une identité n'est crue que sur le périphérique validé par la sonde (une bibliothèque montée sur un autre système de fichiers reste sans inode distant) et elle est relue à chaque usage (une session exec par fichier, jamais de cache). Sinon (refus, programme absent, coreutils non GNU, réponse incohérente) : SFTP seul et un seul avertissement `remote_exec_unavailable` par volume et par session.
- `off` : aucune commande, aucun avertissement. `required` : une sonde en échec refuse les racines d'écriture du volume (`remote_exec_required`) ; Link garde la proposition pour la relance, dedupe la rend non applicable.
- Ce qui manque sans exec : pas d'inode distant (réparation `inode` impossible, un fichier n'est jamais « déjà lié »), pas d'empreinte distante (dedupe relit les deux fichiers en entier par SFTP, au débit du compte, `warning.dedupe.full_remote_read`).
- Avec exec : l'empreinte est calculée sur l'hôte (le serveur lit chaque fichier une fois, sous `nice` et `ionice` s'ils existent ; `remote_hash_bytes` en annonce le volume). Un inode distant ne se compare qu'à un autre inode du même volume, jamais à un fichier local ni à un autre fournisseur ; il ne prouve jamais un lien (`identity_unproven`, pas de `skip_checking`) et une réparation trouvée par inode distant est proposée, jamais appliquée d'office.
- Une commande qui échoue après une sonde réussie (fichier disparu, délai) donne `remote_exec_failed` pour l'élément, relu par SFTP ; dedupe `verify: auto` passe alors à la lecture complète, jamais aux échantillons.
- Compte recommandé : lecture seule sur la bibliothèque, écriture seulement dans le dossier de liens, et une clé dédiée bornée par une commande forcée de garde dans `authorized_keys` : [remote-command-guard.sh](remote-command-guard.sh) (installation en tête du script, vérifié par `shellcheck`). Il ne laisse passer que le serveur SFTP et les lignes exactes de la table, que `necromanciarr volumes commands <name>` écrit aussi, une par ligne et à l'identique, tabulations comprises (même liste dans l'onglet Volumes). `volumes probe <name>` refait la sonde après un changement côté serveur, et `volumes list` montre les capacités obtenues.
