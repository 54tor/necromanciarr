# Limites et dépannage

## Limites de la v1

### Lecture et stockage distant

- Verify lit **tous les octets** des fichiers locaux couverts par les pièces d'un candidat. Le seul frein est `readBytesPerSecond` (20 Mio/s par défaut, rafale 1 Mio) ; la couverture n'a pas de plafond d'octets par candidat (`tieBreakReadBytes` ne borne que le départage des copies de même taille). Durée estimée d'un candidat : taille des fichiers divisée par le débit. Les pièces déjà vérifiées ne sont pas relues à la reprise, mais toute invalidation (fichier modifié, associations changées) repart de zéro.
- Verify traite un candidat à la fois, sans parallélisme.
- Sur un stockage objet monté (rclone, S3) la lecture complète coûte du temps, de la bande passante et, chez certains fournisseurs, du transfert facturé. S3 fournit un MD5 mais pas de SHA-1 : la vérification par pièces relit toujours les fichiers ; seules la déduplication et la réparation évitent la lecture via `fsProviders`.
- `dedupe plan` annonce `bytes_to_read` avant acceptation : 0 si le stockage donne les deux empreintes, la taille d'un fichier s'il en faut une, les deux sinon. Les empreintes début-fin (2 Mio) sont mises en cache par (chemin, taille, date).
- Un montage qui ne restitue pas la date de modification du fichier (backend `mega` classique) désactive la méthode « taille et date » de la réparation ; un objet réécrit change de date et rend les candidats concernés périmés au scan suivant.
- Un renommage par-dessus un fichier n'est pas atomique sur certains volumes (le VFS de rclone peut supprimer la destination avant de renommer) : la déduplication l'écrit en avertissement et `dedupe plan` reprend les entrées temporaires restantes.
- `dedupe` et `repair` lisent et écrivent par le registre des volumes : chaque écriture (lien temporaire, remplacement, retrait, reprise par `dedupe plan`) passe par la racine d'écriture du dossier torrent ou du dossier de liens, refusée si elle recouvre la bibliothèque. Le remplacement exige un remplacement atomique annoncé par le volume : sans lui (serveur SFTP sans `posix-rename`), la proposition dedupe est non applicable (`no_atomic_replace`) et `repair apply` refuse, sans rien écrire et sans repli sur un renommage ou une suppression puis création.
- Sur un volume distant (`sftp://`) sans commandes distantes, dedupe ne dispose d'aucune empreinte du stockage : `verify: auto` fait la preuve par lecture complète des deux fichiers, annoncée avant acceptation par `bytes_to_read` et l'avertissement `warning.dedupe.full_remote_read` ; `verify: hash` rend `hash_unavailable`. Avec `RemoteHash` (sonde `exec`), l'empreinte vient de `sha1sum` ou `md5sum` sur l'hôte (source `sftp-exec`) : rien ne passe par le réseau, `remote_hash_bytes` du plan annonce les octets que le serveur relira (une fois par fichier distant) avec `warning.dedupe.remote_hash_bytes`. Une commande en échec donne `warning.dedupe.remote_exec_failed` au plan ; à l'application, `verify: auto` repasse à la lecture complète (jamais aux échantillons) et `verify: hash` refuse sans lire. L'identité de fichier distante n'est jamais une preuve : un fichier n'est pas « déjà lié » faute d'identité connue, et le lien temporaire est contrôlé par l'empreinte début et fin du fichier de la bibliothèque relue à travers lui. La réparation « par inode » n'est essayée que sur un volume qui annonce l'identité des fichiers (`FileIdentity` en local, `RemoteStat` à distance) ; trouvée par un inode distant, elle n'est jamais appliquée d'office (acceptation explicite). Une identité distante ne se compare qu'à une autre du même volume. Un hardlink dont l'identité est inconnue est `ok` sans comparaison.
- Un `Force` sur un `partial_annex` copie dans le dossier de liens chaque média à cheval sur une pièce manquante : sans reflink, cela coûte la taille du média en place libre (et en lecture sur un stockage distant).
- Scan n'inventorie que les fichiers média, ne suit aucun lien symbolique à l'intérieur d'une racine et ignore un sous-dossier monté sur un autre système de fichiers (sur toutes les plateformes) : déclarer chaque montage comme sa propre racine dans `libraryRoots`.

### Types de volumes

`linker` classe le volume d'un chemin par `statfs` (Linux) : `local`, `fuse` (rclone, sshfs…), `network` (NFS, SMB/CIFS, 9p, Ceph, AFS, Lustre, GFS2, OCFS2, Coda, NCP, OrangeFS) ou `unknown` (autres plateformes). Seul `local` a des inodes stables : sur les autres, l'inode n'est pas conservé en base, la réparation « par inode » est impossible et la déduplication ajoute les avertissements `warning.dedupe.unreliable_inode`, `warning.dedupe.full_network_read`, `warning.dedupe.full_remote_read` (volume `sftp://`), `warning.dedupe.remote_hash_bytes`, `warning.dedupe.remote_exec_failed` ou `warning.dedupe.sampled_verify` (texte traduit dans `warnings` de la proposition). Sous Windows, seuls un lecteur réseau ou un partage UNC sont reconnus `network` ; un montage rclone (WinFsp) y apparaît en lecteur fixe et n'est pas détecté.

### Liens

| Sorte | Contrainte |
|---|---|
| hardlink | même système de fichiers que la source (sinon `EXDEV`, raison `cross_device`) ; absent des volumes FAT et en général d'un montage FUSE ; nombre de liens par fichier borné par le système (`EMLINK`) |
| reflink | Linux seulement (`FICLONE`, btrfs, XFS avec reflink) ; ailleurs `link_kind_refused` ; même système de fichiers |
| symlink | cible **absolue** vue par ce processus (à Link) : le client doit voir la même arborescence, et qBittorrent ne suit pas toujours les symlinks ; sous Windows, privilège ou mode développeur requis |

- Un symlink **relatif** n'existe que pour `dedupe` (`relativeSymlinks`) ; sans chemin relatif possible (volumes Windows distincts) l'application échoue, sans repli sur un chemin absolu.
- `linkType` ne désigne qu'une sorte : si le dossier de liens la refuse (sonde), la proposition échoue ; il n'y a pas de repli hardlink, puis reflink, puis symlink à Link. `dedupe.kinds` est, lui, une liste ordonnée.
- La comparaison d'un reflink ou d'une copie dans `links list` et `repair scan` se fait **par la taille seule** (`Lstat`), jamais par le contenu ; une copie de même taille mais de contenu différent est `ok`. Sans taille enregistrée, le lien est `ok` avec `compared: false` (« Non comparé »).
- Un symlink **repointé** vers une autre cible existante n'est pas détecté.
- `links list` ne modifie pas l'état des liens en base (il reste celui du dernier `repair scan`). Chaque appel réexamine tous les liens (20 s de budget, un examen à la fois) : une liste partielle (`truncated`) peut donner des pages qui ne se raccordent pas exactement ; un `Stat` bloqué par un montage figé garde sa goroutine jusqu'à la réponse du noyau, d'où `inspection_busy`.
- Un seul dossier de liens par torrent : le premier de `linkDirs` sur le même système de fichiers que le premier fichier source.

### Chemins et casse

- Windows : les règles `pathMappings` à lettre de lecteur ou `\\` ignorent la casse et acceptent `\` ou `/`. Le rapprochement des chemins de Radarr et Sonarr avec l'inventaire, lui, est **sensible à la casse** (`D:\Media` n'est pas `d:\media`) : un écart donne `arr_paths_matched_nothing`.
- Les contrôles de chevauchement entre dossiers de liens, racines torrent et bibliothèque sont lexicaux, sensibles à la casse, puis refaits sur chemins résolus par Link et `dedupe`. Un volume insensible à la casse n'est pas géré.
- Un chemin de client Windows qui contient un caractère interdit (`: ? * " < > |` après le lecteur) est refusé (`client_path_invalid`).

### Pièces à cheval et annexes

- Une pièce qui couvre un fichier absent n'est jamais comptée possédée : un torrent complet côté médias reste `partial_annex` si une annexe manque et qu'aucune variante de `.nfo` ne se vérifie. Le plafond de pièces manquantes d'un `partial_annex` n'est pas configurable.
- Une annexe `.nfo` conservée pèse 1 Mio au plus ; un `.torrent` conservé 8 Mio au plus. Un `.torrent` dont la longueur de pièce est nulle, négative ou dépasse 256 Mio est `invalid_torrent`.
- Réponses du client torrent : Seed interroge le client par hash (`torrents/info?hashes=`), jamais la liste entière ; le diagnostic ne lit qu'une page (« torrents : at least 500 » au-delà). Le recontrôle d'un chemin (dedupe, repair) lit la liste **page par page** (`limit=500`, tri `sort=hash`, pages qui se chevauchent d'un torrent) et ne garde de chaque page que les torrents dont le dossier contient le fichier : une seule page est en mémoire à la fois, sans limite de taille de client autre qu'une borne de sûreté de 200 000 torrents (`qbit: more torrents than the listing bound`). Chaque page est plafonnée à 8 Mio (`qbit: response larger than its limit`), comme la liste des fichiers d'un torrent et l'export d'un `.torrent` ; une page illisible, trop grosse, ou une annulation arrête la lecture, rien n'est mis en pause. qBittorrent n'a ni instantané ni filtre `save_path` : si le premier hash d'une page n'est plus le dernier de la précédente (torrent ajouté ou retiré pendant la lecture), la liste est relue en entier, trois fois au plus, puis `qbit: the torrent list kept changing while it was read`. L'ensemble des torrents à couvrir est lu une fois avant la pause ; un torrent ajouté après cette lecture n'est pas mis en pause par cette garde.

### Périmètre

- Seul qBittorrent (et qui par son proxy) est pris en charge ; seul le premier client de `torrentClients` reçoit des torrents. Le tag `necromanciarr` est fixe ; la catégorie est `linkCategory`.
- Match n'interroge que les fichiers qui ont un identifiant TMDB ou TVDB de Radarr ou Sonarr. Une bibliothèque `sftp://` a besoin d'une règle `pathMappings` dont `from` est son adresse, pour que les chemins de Radarr et Sonarr la rejoignent. Torznab et Prowlarr se configurent et `doctor` les vérifie, mais Match ne les interroge pas encore.
- Une proposition acceptée ne s'annule pas ; une proposition rejetée ne se rouvre pas dans la même génération. Un candidat `failed` n'a pas de relance manuelle : il revient à `matched` seulement si ses associations changent.
- Les jobs web ne sont pas persistés ; `dedupe apply` et `repair apply` du web s'appliquent un par un.
- Les titres, détails et conseils de `doctor` suivent `language` ; ses codes ne changent jamais. Les motifs d'échec du serveur restent en anglais jusqu'à leur traduction, et `doctor` affiche des chemins absolus à l'opérateur authentifié.

### Sécurité de l'API

- **Aucune limite de débit sur l'authentification** : un échec écrit une ligne de journal (adresse, sans jeton) et rien d'autre. Garder le jeton long (32 caractères au moins, `api-key` en tire 43) et ne pas exposer le port sans mandataire.
- **Jeton facultatif en boucle locale** : sur une machine partagée entre plusieurs utilisateurs, tout utilisateur local atteint l'API de `127.0.0.1` sans jeton. Définir `NECROMANCIARR_AUTH_TOKEN` même en boucle locale.

### Base et plateformes

- **Journal SQLite** : la base garde le mode de journal par défaut (`DELETE`), sans WAL. Le mode WAL exige une mémoire partagée (`-shm`) que les volumes réseau (NFS, SMB, montages rclone) ne garantissent pas, et la base vit souvent sur un tel volume en Docker ; les transactions prennent le verrou d'écriture dès `BEGIN` (`_txlock=immediate`) avec une attente de 5 secondes (`busy_timeout`).
- **Windows** : les binaires `windows/amd64` et `windows/arm64` sont construits ; la CI n'exécute sur Windows que les tests des paquets `internal/linker`, `internal/config` et `internal/pipeline` (job `windows-tests`, non bloquant tant que son premier passage n'est pas trié), sur `amd64`.

### Risques acceptés (relecture de la v1)

| Risque | Condition |
|---|---|
| `doctor` suit un lien symbolique entre le contrôle de chevauchement et la sonde (TOCTOU) | un tiers modifie les dossiers de liens pendant le diagnostic |
| À Link, l'adoption d'une entrée déjà présente ne rejoue pas le contrôle de confinement par segments | un tiers a déjà écrit dans le dossier de liens de l'outil |
| L'écriture d'une annexe renomme par-dessus sans `fsync` du dossier parent | coupure franche à cet instant |
| Une clé dupliquée de `config.json` n'est pas détectée (la dernière gagne) | limite d'`encoding/json` |
| Dépendances de développement : 5 alertes `npm audit` de niveau modéré | dev seulement (le seuil bloquant est `high`) |

## Dépannage

### Par contrôle `doctor`

Chaque résultat porte un `code` stable en snake_case, identique en français et en anglais : un script se fie au `code` et au `status`, jamais au `detail` ni au `hint`, qui sont traduits (`language`). Un contrôle qui examine plusieurs éléments (racines, dossiers, clients, règles) porte le code de son pire constat, le premier à statut égal ; tous les constats restent dans `detail`, séparés par « ; ».

| Contrôle | Code | Statut | Cause et action |
|---|---|---|---|
| tous | `ok` | `ok` | rien à faire |
| tous | `check_timeout` | `fail` | le contrôle a dépassé son délai (`WithTimeout`, 10 s par défaut) : service lent ou bloqué |
| tous | `cancelled` | `skipped` | diagnostic interrompu avant ce contrôle |
| tous | `panic` | `fail` | bogue : le type et un message masqué sont donnés ; le signaler |
| `config` | `working_dir_dotenv` | `warn` | un `.env` du dossier de travail a été lu : le déplacer dans le dossier de configuration, ou le supprimer |
| `config` | `tracker_value_too_short` | `warn` | une variable de `trackers` fait moins de 8 caractères : sa valeur n'est masquée que dans les URL (quel que soit le nom du paramètre, à l'identique), jamais dans le texte libre ; `config check` la nomme aussi |
| `config` | `config_invalid` | `fail` | `config.json` invalide (`unknown field …`, `parse config.json`, `undefined variable X`, `linkDirs[i] … overlaps libraryRoots[j]`, `usage: libraryRoots[i]: … local path is not absolute` ou `… contains ..`, `… provider "x" is not declared in fsProviders`, `… provider "x" has type "rclone", which does not expose a filesystem`, `fsProviders[i]: usage: type sftp: knownHosts is required …`, `… password must be a single ${VARIABLE} reference, never a literal value`, `usage: dbPath: the database stays on a local disk, never on an sftp:// location`) : corriger, puis `config check` |
| `config-dir` | `config_dir_missing` | `fail` | dossier de configuration absent : le créer ou passer `--config-dir` |
| `config-dir` | `database_dir_unresolved` | `fail` | dossier de la base introuvable : le créer ou corriger `dbPath` |
| `config-dir` | `database_in_library` | `fail` | `dbPath` dans une racine de bibliothèque : le placer ailleurs |
| `config-dir` | `database_unwritable` | `fail` | droits ou espace sur le dossier de la base ; en Docker, propriétaire de `/data` |
| `auth` | `loopback` | `ok` | écoute en boucle locale |
| `auth` | `auth_token_set` | `ok` | écoute hors boucle locale avec jeton |
| `auth` | `no_auth_token` | `fail` | écoute hors boucle locale sans jeton, que `serve` refusera : `necromanciarr api-key`, puis `.env` |
| `library-roots` | `no_library_root` | `warn` | `libraryRoots` vide |
| `library-roots` | `root_not_found` | `fail` | montage absent ou chemin faux |
| `library-roots` | `root_not_directory` | `fail` | la racine n'est pas un dossier |
| `library-roots` | `root_unreadable` | `fail` | droits de l'utilisateur du processus |
| `library-roots` | `root_empty` | `warn` | aucun média dans les 2000 premières entrées : racine vide ou montage pas encore prêt |
| `library-roots` | `root_ok` | `ok` | racine lisible, média trouvé |
| `library-api` | `library_api_unset` | `skipped` | `libraryApi` non configuré |
| `library-api` | `provider_client_failed` | `fail` | `libraryApi` ou `LIBRARY_PROVIDER_TOKEN` inutilisable |
| `library-api` | `provider_access_denied` | `fail` | `LIBRARY_PROVIDER_TOKEN` faux ou absent |
| `library-api` | `provider_not_found` | `fail` | chemin de base de `libraryApi` |
| `library-api` | `provider_rate_limited`, `provider_unavailable` | `fail` | quota ou panne du fournisseur ; relancer plus tard |
| `library-api` | `provider_bad_response` | `fail` | réponse hors [`openapi.yaml`](../contract/openapi.yaml) |
| `library-api` | `provider_timeout`, `provider_request_failed` | `fail` | réseau ou fournisseur ; vérifier `libraryApi` |
| `library-api` | `size_filter_ignored` | `warn` | fournisseur sans filtre `size` : fonctionne, beaucoup plus de candidats |
| `library-api` | `size_filter_unverifiable` | `ok` | aucun résultat exploitable pour vérifier le filtre |
| `arr:radarr:0` | `api_key_rejected` | `fail` | `?apikey=` de l'instance |
| `arr:radarr:0` | `timed_out`, `unreachable` | `fail` | URL, instance démarrée |
| `torznab:12` | `torznab_ok` | `ok` | capacités lues, clé acceptée (seul `t=caps` est demandé : aucune recherche ni téléchargement n'est compté) |
| `torznab:12` | `torznab_access_denied` | `fail` | `?apikey=` de l'entrée `torznab` (HTTP 401 ou 403, ou erreur Torznab 100 à 199) |
| `torznab:12` | `torznab_unreachable` | `fail` | URL, indexeur ou Prowlarr démarré ; quota atteint (HTTP 429 ou 503, erreurs Torznab 429, 500, 501, 503, 900, 910) |
| `torznab:12` | `torznab_hostile_response` | `fail` | réponse refusée (`DOCTYPE` ou `ENTITY`, imbrication trop profonde, plus de 500 éléments) : l'URL ne désigne pas un flux de confiance |
| `torznab:12` | `torznab_bad_response` | `fail` | pas un flux Torznab : chemin erroné (Prowlarr : `/<id>/api`) |
| `torznab:12` | `torznab_not_in_source_order` | `warn` | l'indexeur manque dans `sourceOrder` : il n'est jamais interrogé ; l'y ajouter ou le retirer de `torznab` |
| `torznab:12` | `torznab_no_media_mode` | `warn` | ni `movie-search` ni `tv-search` : l'indexeur ne donnera aucun candidat |
| `prowlarr` | `prowlarr_ok` | `ok` | liste des indexeurs lue, chaque entrée `torznab` de Prowlarr y est activée |
| `prowlarr` | `prowlarr_access_denied` | `fail` | `?apikey=` de `prowlarr` (Settings > General > API Key) |
| `prowlarr` | `prowlarr_unreachable`, `prowlarr_bad_response` | `fail` | `prowlarr` est la racine de l'API native (`http://host:9696`, plus la base d'URL), pas un flux ; Prowlarr démarré |
| `prowlarr` | `prowlarr_indexer_missing_or_disabled` | `warn` | une entrée `torznab` dont l'identifiant du chemin est absent ou désactivé dans Prowlarr : l'activer ou la retirer ; le téléchargement exige que Prowlarr relaie le fichier (mode Proxy, option `Redirect` désactivée sur l'indexeur) |
| `secret-key` | `secret_key_ok` | `ok` | `NECROMANCIARR_SECRET_KEY` présente (32 octets) et chaque `.torrent` scellé lu en base (1000 blocs et 64 Mio au plus par diagnostic ; base absente : aucun) se descelle, la liaison reprise de la ligne du candidat |
| `secret-key` | `secret_key_missing` | `fail` | `torznab` configuré sans `NECROMANCIARR_SECRET_KEY` : la configuration est refusée ; en générer une avec `necromanciarr api-key --secret` |
| `secret-key` | `secret_key_wrong_length` | `fail` | la valeur n'est pas 32 octets en base64url sans remplissage (seule l'écriture d'`api-key --secret` est admise) |
| `secret-key` | `sealed_torrent_unreadable` | `fail` | un `.torrent` scellé ne se descelle plus (clé changée ou bloc altéré) : Seed le refusera ; relancer Verify sur ce candidat |
| `secret-key` | `sealed_torrents_unchecked` | `warn` | les `.torrent` scellés n'ont pas pu être lus en base (base illisible ou d'une version antérieure), ou certains dépassent le budget du diagnostic (1000 blocs, 64 Mio) et ne sont pas vérifiés ; la clé elle-même est conforme |
| `qbittorrent` | `no_torrent_client` | `skipped` | aucun client configuré |
| `qbittorrent` | `client_build_failed` | `fail` | URL du client inutilisable |
| `qbittorrent` | `client_login_rejected` | `fail` | identifiants de l'URL ; encoder en pourcentage `@ : / # ?` du mot de passe |
| `qbittorrent` | `client_timeout`, `client_unreachable` | `fail` | URL, client démarré |
| `qbittorrent` | `client_list_failed` | `fail` | connexion réussie, liste des torrents refusée |
| `qbittorrent` | `client_ok`, `seed_settings` | `ok` | client joignable ; catégorie et étiquettes d'ajout |
| `qbittorrent` | `client_type_unsupported` | `warn` | client importé de cross-seed autre que qBittorrent : ignoré |
| `link-feasibility` | `no_link_directory` | `skipped` | ni `linkDirs` ni racine torrent |
| `link-feasibility` | `link_dir_unresolved` | `fail` | dossier de liens introuvable : aucune sonde lancée |
| `link-feasibility` | `link_dir_in_library` | `fail` | dossier de liens dans la bibliothèque : déplacer `linkDirs` |
| `link-feasibility` | `link_probe_failed` | `fail` | dossier non accessible en écriture |
| `link-feasibility` | `library_root_unavailable` | `fail` | une racine de bibliothèque est absente ou non montée (ou aucune n'est configurée) : aucune racine d'écriture n'est ouverte, ni sonde ni lien |
| `link-feasibility` | `link_kinds_ok` | `ok` | sortes de liens voulues acceptées (✓ accepté, ✗ refusé, avec le motif) |
| `link-feasibility` | `link_kind_fallback` | `warn` | la sorte préférée est refusée, une suivante est acceptée ; avec `symlink` refusé sur un montage rclone : relancer rclone avec `--vfs-links` |
| `link-feasibility` | `link_kinds_refused` | `fail` | aucune sorte voulue n'est acceptée |
| `link-feasibility` | `volumes_not_comparable` | `warn` | un des deux chemins n'existe pas |
| `link-feasibility` | `same_volume` | `ok` | bibliothèque et liens sur un même volume |
| `link-feasibility` | `volumes_differ_symlink_only` | `warn` | deux volumes : seuls les symlinks traversent |
| `link-feasibility` | `volumes_differ` | `fail` | bibliothèque et liens sous deux montages : un seul montage commun ([05-docker-and-volumes.md](05-docker-and-volumes.md)) |
| `fs-providers` | `no_fs_provider` | `skipped` | aucun fournisseur configuré |
| `fs-providers` | `fs_providers_build_failed` | `fail` | `fsProviders` de `config.json` |
| `fs-providers` | `mount_ok` | `ok` | montage décrit |
| `fs-providers` | `mount_advice` | `warn` | le conseil du fournisseur (clés `advice.mount.*` de `hashsource`, rendues dans la langue) : démarrer rclone avec `--rc`, backend sans MD5 ou sans date (la comparaison relit les fichiers) |
| `cross-seed` | `cross_seed_unset` | `skipped` | cross-seed non configuré |
| `cross-seed` | `cross_seed_mode_invalid` | `fail` | `crossSeedMode` inconnu |
| `cross-seed` | `cross_seed_warning` | `warn` | démon injoignable : repli `native` en mode `auto`, désactivé en `delegate` |
| `cross-seed` | `blocked_environment` | `warn` | `config.js` lit des variables de cet outil, qui lui sont cachées |
| `path-mappings` | `no_path_mapping` | `skipped` | aucune règle |
| `path-mappings` | `rule_unreachable` | `warn` | ni `from` ni `to` n'existe sur cette machine |
| `path-mappings` | `rule_no_library_root` | `warn` | règle qui ne couvre aucune racine |
| `path-mappings` | `rule_ok` | `ok` | règle utilisable |
| `dedupe` | `dedupe_inactive` | `skipped` | `dedupe.torrentRoots` vide |
| `dedupe` | `torrent_root_overlaps_library` | `fail` | `torrentRoots` à déplacer hors de la bibliothèque |
| `dedupe` | `torrent_root_missing` | `warn` | dossier introuvable sur cette machine |
| `dedupe` | `torrent_root_ok` | `ok` | racine utilisable |

### Par raison d'étape (`reasons`) et propositions échouées

| Raison | Étape | Cause et action |
|---|---|---|
| `no_external_id` | Scan | fichier absent de Radarr/Sonarr ou chemin mal traduit : vérifier `radarr`, `sonarr`, `pathMappings` |
| `arr_paths_matched_nothing` | Scan | aucun chemin de l'instance ne correspond : règle fausse, casse Windows |
| `empty_root_not_pruned` | Scan | tous les fichiers d'une racine manquent : montage décroché ? |
| `search_failed`, `update_deferred_incomplete_run` | Match | recherche en échec : relancer `match` |
| `changed_file_stale`, `stale_candidate` | Match, Verify | fichier modifié : relancer `scan`, `match`, `verify` |
| `media_hash_failed` | Verify | un média présent n'a pas les octets du torrent (ré-encodé, corrompu, autre release) : aucun lien |
| `read_budget_exceeded` | Verify | départage des copies de même taille arrêté à `tieBreakReadBytes` : les copies restent en litige ; supprimer les doublons ou relever la valeur |
| `no_file_matched`, `no_exact_size` | Verify, Match | aucun fichier local de la bonne taille |
| `v2_only`, `invalid_torrent`, `infohash_mismatch` | Verify | `.torrent` inutilisable ou qui n'est pas celui annoncé |
| `torrent_not_found`, `torrent_fetch_failed`, `torrent_too_large` | Verify | fournisseur : 404, panne, `.torrent` de plus de 8 Mio ; après le secours, aucun indexeur du tracker n'a ce `.torrent` (voir `searched_sources`, et `tracker` de chaque indexeur) |
| `indexer_unavailable`, `indexer_access_denied`, `indexer_hostile_response` | Verify, Match | indexeur en panne, limité (`Retry-After`), clé d'API refusée, ou réponse hostile : l'indexeur est écarté pour le reste de l'exécution ; dans Verify, le candidat reste `matched` et sera repris, dans Match, les groupes restants le seront à la prochaine exécution ; `doctor` (`torznab:<name>`) |
| `torrent_fetch_failed` | Verify | secours : réponse illisible d'un indexeur à une recherche (page HTML, mauvais type, corps trop gros) ; même effet que la ligne précédente |
| `magnet_only`, `foreign_download_host` | Verify, Match | éléments d'indexeur ignorés : magnet seul, ou lien de téléchargement hors de l'hôte de l'indexeur |
| `torznab_no_exact_size` | Match | `.torrent` d'indexeur sans fichier de la taille exacte d'un fichier local (une taille totale juste ne suffit pas) : écarté, jamais enregistré ; le résultat suivant est essayé |
| `secret_key_missing` | Match | `NECROMANCIARR_SECRET_KEY` absente : aucun indexeur n'est sollicité (compté une fois) |
| `read_error` | Verify | lecture locale en échec : reprise à la relance |
| `no_link_dir_on_same_device` | Link | aucun `linkDirs` sur le volume de la source ; `linkType` n'est pas `symlink` |
| `link_kind_refused`, `cross_device` | Link | le dossier refuse la sorte (voir `doctor`), ou deux volumes |
| `destination_conflict` | Link | le dossier du torrent contient une entrée qui n'est pas de l'outil : rien n'est supprimé, la regarder |
| `link_root_unusable`, `link_dir_overlaps_library` | Link | dossier absent, non inscriptible ou dans la bibliothèque |
| `library_root_unavailable` | Link | une racine de bibliothèque est absente ou non montée : aucune racine d'écriture n'est ouverte, `doctor` le signale (`library_root_unavailable`) |
| `source_changed`, `changed_file_stale` | Link, Seed | source modifiée : revérifier |
| `annex_write_failed`, `link_failed` | Link | écriture refusée ou erreur système : droits, espace |
| `not_linked` | Seed | proposition acceptée mais Link pas passé |
| `already_in_client` | Seed | l'infohash existe dans le client à un autre chemin : rien n'est touché |
| `client_read_only` | Seed | client déclaré `readonly:` |
| `add_failed`, `recheck_failed`, `resume_failed`, `pause_failed` | Seed | client injoignable ou refus : `doctor` (`qbittorrent`), relancer |
| `not_paused_after_add` | Seed | torrent incomplet introuvable, ou démarré malgré deux pauses (alors retiré du client, données gardées) : vérifier le client, puis réaccepter et relancer Seed |
| `incomplete_after_recheck`, `paused_incomplete` | Seed | torrent laissé en pause (voir [03-seed-safety.md](03-seed-safety.md)) |
| `link_changed`, `annex_changed` | Seed | dossier du torrent modifié depuis Link : réaccepter et relancer `link` |
| `client_path_invalid` | Seed | chemin client avec caractère interdit sous Windows |
| `client_path_unmapped` | Seed, `dedupe apply`, `repair apply` | dossier de liens distant sans règle `pathMappings` dont `from` est une adresse `sftp://` du même fournisseur : ajouter la règle vers la vue du client, réaccepter puis relancer. dedupe et repair refusent avant toute écriture quand un recontrôle du client est prévu |
| `no_atomic_replace` | `dedupe plan` | le volume du dossier torrent ne remplace pas un fichier de façon atomique (SFTP sans `posix-rename`) : proposition non applicable, rien n'est écrit |
| `remote_exec_required` | Link, `dedupe plan` | volume réglé `exec: required` dont la sonde des commandes distantes a échoué : racine d'écriture refusée, rien n'est écrit ; Link garde la proposition pour la relance, dedupe la rend non applicable. Autoriser les lignes de `volumes commands` (garde de commande), ou passer `exec` à `auto` |
| `remote_exec_failed` | dedupe, repair, identité de fichier | une commande distante a échoué après une sonde réussie (fichier disparu, délai, sortie non conforme) : l'élément est relu par SFTP (dedupe : lecture complète ; repair : empreinte début et fin ; identité : inconnue). Rien à faire si c'est rare ; sinon voir `doctor` |
| `identity_unproven` | Seed | lien distant : identité jamais prouvée, `skip_checking` refusé, le client recontrôle et le torrent reste en pause jusqu'à 100 % (aucune action) |
| `remote_unavailable` | Scan, Verify, Link, Seed | le volume distant ne répond pas : racine de bibliothèque distante laissée telle quelle par Scan, candidat et proposition gardés tels quels (jamais `stale` ni `failed`, aucune lecture comptée comme différente), relancer l'étape une fois le volume revenu |
| `symlink_order_unverified` | Link | le serveur SFTP n'a pas prouvé l'ordre des arguments d'un lien symbolique (sonde inversée ou sans verdict) : liens symboliques refusés sur ce volume, proposition en échec. Choisir `hardlink` dans `linkKinds`, ou corriger le serveur (OpenSSH lit l'ordre attendu), puis réaccepter |
| `server_copy_unavailable` | Force | dossier de liens sur un volume SFTP sans copie côté serveur (`copy-data`) : rien n'est copié ni changé, le torrent reste en pause. Lancer l'outil sur la seedbox avec un dossier de liens local, ou attendre la prise en charge de `copy-data` ([03-seed-safety.md](03-seed-safety.md)) |
| `free_space_unknown` | Force | la place libre du dossier de liens distant est illisible (serveur sans `statvfs`) : rien n'est copié ni changé ; activer l'extension `statvfs` du serveur (seulement une fois `copy-data` pris en charge) |
| `crossseed_notify_failed` | Seed | démon refuse ou injoignable : avertissement seulement |
| `no_announce` | Verify, Link, Seed | ni entrée `trackers` ni `.torrent` scellé d'indexeur : ajouter l'entrée `trackers`, puis réaccepter (aucune revérification) |
| `sealed_torrent_unreadable`, `secret_key_missing` | Seed | `.torrent` scellé illisible (clé changée) ou clé absente : remettre `NECROMANCIARR_SECRET_KEY`, ou relancer Verify sur le candidat |
| `infohash_changed_after_inject` | Seed | le `.torrent` construit n'a plus l'infohash du candidat : rien n'est ajouté, relancer Verify |
| `indexer_budget_exhausted` | Verify, Match | un plafond de requêtes à un indexeur (par exécution ou par jour, fenêtre glissante de 24 h) est atteint : ce n'est pas un échec, la suite reprend à la prochaine exécution ; le décompte par jour survit aux redémarrages |

### Par code de connexion SFTP

Codes stables d'une connexion à un fournisseur `sftp`, identiques dans les deux langues.

| Code | Gravité | Cause et action |
|---|---|---|
| `host_key_unknown` | refus | l'hôte n'a aucune entrée dans `knownHosts` : ajoutez la clé avec `ssh-keyscan -H seedbox.example.com >> known_hosts` (votre hôte), après avoir vérifié l'empreinte par un autre canal (voir « Obtenir la ligne `known_hosts` » dans la configuration) |
| `host_key_mismatch` | refus | la clé présentée diffère de celle épinglée, ou elle est révoquée : connexion peut-être interceptée. Ne rien remplacer avant d'avoir vérifié la nouvelle empreinte sur le serveur par un autre canal ; ensuite seulement, remplacer la ligne |
| `auth_refused` | refus | le serveur a refusé toutes les méthodes configurées (`agent`, `identityFile`, `password`), ou aucune n'était utilisable : vérifier `user`, la clé autorisée côté serveur (`authorized_keys`), la variable du mot de passe |
| `known_hosts_unreadable` | refus | `knownHosts` absent, illisible ou sans aucune entrée valide : vérifier le chemin (vu par le processus ou le conteneur), les droits de lecture et le format OpenSSH |
| `identity_key_too_weak` | refus | `identityFile` est une clé RSA de moins de 3072 bits : générer une clé ed25519 (`ssh-keygen -t ed25519`) et l'autoriser sur le serveur |
| `identity_file_permissions` | avertissement | `identityFile` est lisible ou modifiable par le groupe ou les autres : `chmod 600` ; la connexion continue |
| `agent_unavailable` | avertissement | `agent` est demandé mais `SSH_AUTH_SOCK` est absent ou injoignable, ou l'agent ne répond pas dans `timeout` (jamais transmis dans Docker) : la méthode est sautée, les autres sont essayées |
| `remote_exec_unavailable` | avertissement | `exec: auto`, mais les commandes distantes sont refusées, absentes, pas GNU ou incohérentes avec SFTP (raison dans `remoteExec`) : lecture SFTP seule (pas d'inode ni d'empreinte distante), un seul avertissement par volume et par session |
| `remote_exec_failed` | avertissement | une commande a échoué après une sonde réussie ; le compte dit combien d'éléments ont été relus par SFTP |
| `link_dir_world_writable` | avertissement | des dossiers créés dans un dossier de liens distant sont modifiables par le groupe ou les autres (un seul avertissement par dossier de liens, avec leur nombre) : régler le umask du compte SFTP à `022` (par exemple `ForceCommand internal-sftp -u 022` côté OpenSSH) ou créer ces dossiers à l'avance ; l'opération continue |

### Par raison d'exécution distante (`remoteExec`)

Raison de coupure des commandes distantes d'un volume `sftp`, dans les capacités du volume (`remoteExec`) ; le volume continue en SFTP seul (lectures complètes, pas d'inode ni d'empreinte distante), sauf avec `exec: required`.

| Code | Cause et action |
|---|---|
| `exec_off` | `exec` vaut `off` : aucune commande n'est envoyée |
| `exec_denied` | le serveur refuse exec (compte restreint, `ForceCommand internal-sftp`, garde de commande) : normal pour un compte SFTP seul ; autoriser les lignes exactes de la configuration pour en profiter |
| `login_shell_not_posix` | la sonde du shell de connexion a fini sans rendre exactement sa sortie POSIX (fish, csh) : donner au compte un shell POSIX (`chsh -s /bin/sh`) ; aucune autre ligne n'est envoyée de la session |
| `exec_probe_failed` | une ligne de sonde n'a pas fini (délai, sortie démesurée, canal coupé) : SFTP seul pour l'étape, sonde refaite à l'étape suivante ; ou `stat` absent, en échec ou pas GNU coreutils (BusyBox, BSD) : non pris en charge, SFTP seul |
| `exec_program_missing` | `sha1sum` ou `md5sum` absent ou pas GNU coreutils : les empreintes distantes sont indisponibles, `stat` reste utilisé |
| `exec_inconsistent` | les commandes répondent, mais `stat` ou une empreinte contredit SFTP sur les fichiers de sonde (taille, date, inode nul, lien dur vu comme deux fichiers, empreinte fausse), ou le serveur n'a ni lien dur ni remplacement atomique pour poser un fichier de sonde : système de fichiers aux inodes inutilisables (FUSE, union), SFTP seul |

### Par code d'erreur

| Code | Cause et action |
|---|---|
| `stale_proposal` | génération périmée : relancer `scan`, `match`, `verify` puis décider sur la nouvelle proposition |
| `refused` | lire le message : litige à `choose`, pack partiel sans `--allow-partial-pack`, `the proposal must be accepted first` pour dedupe et repair, nature incorrecte, hors racines, client en lecture seule |
| `not_configured` | composant absent : `libraryApi`, `torrentClients` ou `dedupe.torrentRoots` |
| `stage_running` | une autre étape ou action tourne ; attendre, ou 2 minutes après la mort du processus |
| `pipeline: run lock lost` | le verrou a été repris : relancer l'étape |
| `inspection_busy` | un examen des liens est en cours : réessayer |
| `confirmation_required` | `proposals force` sans `--yes`, ou `force` sans `{"confirm": true}` |
| `unauthorized` | `Authorization: Bearer <token>` absent ou faux |
| `forbidden` | `Host` inconnu (proxy qui réécrit `Host` sur une écoute en boucle locale), `Origin` étranger, `Sec-Fetch-Site: cross-site` |
