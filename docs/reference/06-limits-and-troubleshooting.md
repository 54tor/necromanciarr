# Limites et dépannage

## Limites de la v1

### Lecture et stockage distant

- Verify lit **tous les octets** des fichiers locaux couverts par les pièces d'un candidat. Le seul frein est `readBytesPerSecond` (20 Mio/s par défaut, rafale 1 Mio) ; la couverture n'a pas de plafond d'octets par candidat (`tieBreakReadBytes` ne borne que le départage des copies de même taille). Durée estimée d'un candidat : taille des fichiers divisée par le débit. Les pièces déjà vérifiées ne sont pas relues à la reprise, mais toute invalidation (fichier modifié, associations changées) repart de zéro.
- Verify traite un candidat à la fois, sans parallélisme.
- Sur un stockage objet monté (rclone, S3) la lecture complète coûte du temps, de la bande passante et, chez certains fournisseurs, du transfert facturé. S3 fournit un MD5 mais pas de SHA-1 : la vérification par pièces relit toujours les fichiers ; seules la déduplication et la réparation évitent la lecture via `fsProviders`.
- `dedupe plan` annonce `bytes_to_read` avant acceptation : 0 si le stockage donne les deux empreintes, la taille d'un fichier s'il en faut une, les deux sinon. Les empreintes début-fin (2 Mio) sont mises en cache par (chemin, taille, date).
- Un montage qui ne restitue pas la date de modification du fichier (backend `mega` classique) désactive la méthode « taille et date » de la réparation ; un objet réécrit change de date et rend les candidats concernés périmés au scan suivant.
- Un renommage par-dessus un fichier n'est pas atomique sur certains volumes (le VFS de rclone peut supprimer la destination avant de renommer) : la déduplication l'écrit en avertissement et `dedupe plan` reprend les entrées temporaires restantes.
- Un `Force` sur un `partial_annex` copie dans le dossier de liens chaque média à cheval sur une pièce manquante : sans reflink, cela coûte la taille du média en place libre (et en lecture sur un stockage distant).
- Scan n'inventorie que les fichiers média, ne suit aucun lien symbolique à l'intérieur d'une racine et ignore un sous-dossier monté sur un autre système de fichiers : déclarer chaque montage comme sa propre racine dans `libraryRoots`.

### Types de volumes

`linker` classe le volume d'un chemin par `statfs` (Linux) : `local`, `fuse` (rclone, sshfs…), `network` (NFS, SMB/CIFS, 9p, Ceph, AFS, Lustre, GFS2, OCFS2, Coda, NCP, OrangeFS) ou `unknown` (autres plateformes). Seul `local` a des inodes stables : sur les autres, l'inode n'est pas conservé en base, la réparation « par inode » est impossible et la déduplication ajoute les avertissements `warning.dedupe.unreliable_inode`, `warning.dedupe.full_network_read` ou `warning.dedupe.sampled_verify` (texte traduit dans `warnings` de la proposition). Sous Windows, seuls un lecteur réseau ou un partage UNC sont reconnus `network` ; un montage rclone (WinFsp) y apparaît en lecteur fixe et n'est pas détecté.

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
- Match n'interroge que les fichiers qui ont un identifiant TMDB ou TVDB de Radarr ou Sonarr. SFTP, Torznab et Prowlarr ne sont pas pris en charge.
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
| `config` | `config_invalid` | `fail` | `config.json` invalide (`unknown field …`, `parse config.json`, `undefined variable X`, `linkDirs[i] … overlaps libraryRoots[j]`) : corriger, puis `config check` |
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
| `torrent_not_found`, `torrent_fetch_failed`, `torrent_too_large` | Verify | fournisseur : 404, panne, `.torrent` de plus de 8 Mio |
| `read_error` | Verify | lecture locale en échec : reprise à la relance |
| `no_link_dir_on_same_device` | Link | aucun `linkDirs` sur le volume de la source ; `linkType` n'est pas `symlink` |
| `link_kind_refused`, `cross_device` | Link | le dossier refuse la sorte (voir `doctor`), ou deux volumes |
| `destination_conflict` | Link | le dossier du torrent contient une entrée qui n'est pas de l'outil : rien n'est supprimé, la regarder |
| `link_root_unusable`, `link_dir_overlaps_library` | Link | dossier absent, non inscriptible ou dans la bibliothèque |
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
| `crossseed_notify_failed` | Seed | démon refuse ou injoignable : avertissement seulement |

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
