# Configuration

La configuration tient dans un dossier (le « dossier de configuration ») qui contient `config.json`, `.env` et, par défaut, la base SQLite. `necromanciarr config check` affiche la configuration effective, secrets masqués.

## Sources et priorité

Du plus fort au plus faible : **options de ligne de commande**, **variables d'environnement**, **`config.json`**, **`config.js` de cross-seed** (si un chemin est fourni), **défauts**.

| Réglage | Option | Variable | Clé de `config.json` |
|---|---|---|---|
| Dossier de configuration | `--config-dir` | `CONFIG_DIR` | aucune |
| Adresse d'écoute | `--listen` | `NECROMANCIARR_LISTEN` | `listen` |
| Fournisseur de bibliothèque | `--library-api` | `LIBRARY_API` | `libraryApi` |
| Base SQLite | `--db-path` | aucune | `dbPath` |
| Débit de lecture | `--read-bytes-per-second` | aucune | `readBytesPerSecond` |
| Mode cross-seed | `--cross-seed-mode` | `CROSS_SEED_MODE` | `crossSeedMode` |
| `config.js` de cross-seed | `--cross-seed-config` | `CROSS_SEED_CONFIG` | `crossSeedConfig` |
| Catégorie des seeds | aucune | `NECROMANCIARR_SEED_CATEGORY` | `linkCategory` |
| Tags des seeds | aucune | `NECROMANCIARR_SEED_TAGS` (séparés par des virgules) | `seedTags` |
| Langue d'affichage | `--language` | `NECROMANCIARR_LANGUAGE` | `language` |
| Plafond de pièces manquantes | aucune | `NECROMANCIARR_PARTIAL_MAX_MISSING_PERCENT` | `partialMaxMissingPercent` |
| Jeton de l'interface | aucune | `NECROMANCIARR_AUTH_TOKEN` | aucune (jamais dans un fichier JSON) |
| Clé de scellement | aucune | `NECROMANCIARR_SECRET_KEY` | aucune (jamais dans un fichier JSON) |
| Jeton du fournisseur | aucune | `LIBRARY_PROVIDER_TOKEN` | aucune |

Une variable vide est ignorée. Ces options s'ajoutent à toute sous-commande ; une option inconnue est un défaut d'usage (exit 2).

Le dossier de configuration doit exister quand il est donné. Sans option ni variable, c'est `<user config directory>/necromanciarr` (`~/.config/necromanciarr` sous Linux). L'image Docker fixe `CONFIG_DIR=/data`.

## `.env` et interpolation

- Un seul fichier `.env` est lu : celui du dossier de configuration. S'il n'existe pas, celui du dossier courant est lu à sa place ; `config check` le signale (`workingDirDotEnv: true`) et `doctor` avertit (contrôle `config`). Risque : lancé depuis un dossier qui contient le `.env` d'un autre outil, Necromanciarr en reprendrait les variables (jetons compris) ; placer son `.env` dans le dossier de configuration ferme cette porte. L'environnement réel l'emporte toujours sur `.env`.
- Format : `NAME=value`, une par ligne, commentaires `#`, préfixe `export` toléré, guillemets simples ou doubles retirés. Pas de valeur multiligne, pas d'interpolation dans `.env`.
- Dans `config.json`, toute chaîne (y compris dans `dedupe` et `fsProviders`) peut contenir `${NAME}` ; `$$` donne un `$`. Une variable absente ou un nom invalide (hors `[A-Za-z_][A-Za-z0-9_]*`) est une erreur qui cite le nom, jamais la valeur.
- L'interpolation précède l'analyse des URL : encoder en pourcentage les caractères réservés (`@ : / # ?`) d'un mot de passe placé dans une URL.
- `config.json` est lu en mode strict : une clé inconnue est refusée, ainsi que tout texte après l'objet JSON. Une clé dupliquée n'est pas détectée (la dernière gagne). L'ancienne section `rclone` est refusée avec un message qui renvoie à `fsProviders`.

## Clés de `config.json`

| Clé | Type | Défaut | Validation |
|---|---|---|---|
| `listen` | `host:port` | `127.0.0.1:8787` | hors boucle locale, `serve` exige `NECROMANCIARR_AUTH_TOKEN` ; les autres commandes non |
| `dbPath` | chemin | `necromanciarr.db` | relatif : résolu depuis le dossier de configuration. Doit rester hors de la bibliothèque (`doctor`). Toujours local : une adresse `sftp://` (fichier ou `--db-path`) est refusée au chargement |
| `readBytesPerSecond` | entier (octets/s) | `20971520` (20 Mio/s) | strictement positif |
| `tieBreakReadBytes` | entier (octets) | `53687091200` (50 Gio) | strictement positif. Octets lus au plus, par candidat, pour départager des fichiers locaux de même taille ; la couverture n'est pas concernée |
| `libraryApi` | URL | vide | `http(s)`, sans fragment. Vide : Match et Verify refusent (`not_configured`) |
| `libraryRoots` | liste de chemins | vide | vus par ce processus ; chemins absolus, sans segment `..` (refus au chargement). Une adresse `sftp://<provider>/<absolute path>` vise un fournisseur `fsProviders` de type `sftp` déclaré (exemple : `sftp://seedbox/srv/media`) ; un fournisseur `rclone` est refusé. Pour l'instant, `config check` accepte la forme `sftp://` mais le démarrage la refuse tant que le volume SFTP n'est pas disponible |
| `pathMappings` | liste `{from, to}` | vide | `from` : vue de ce processus (chemin local, ou adresse `sftp://<provider>/<absolute path>` d'un fournisseur déclaré) ; `to` : vue de Radarr, Sonarr et du client torrent, jamais une adresse `sftp://` |
| `radarr`, `sonarr` | liste d'URL | vide | `http(s)://host:port[/path]?apikey=KEY`, `user:password@` facultatif (proxy en authentification basique) ; seul `apikey` est admis |
| `torrentClients` | liste de chaînes | vide | `qbittorrent[:readonly]:http(s)://[user:password@]host:port[/path]`, sans paramètre de requête. Seul le premier client reçoit les torrents (avertissement s'il y en a plusieurs) |
| `linkDirs` | liste de chemins | vide | chemins absolus sans `..` ; ne chevauche jamais une racine de `libraryRoots` du même volume |
| `linkType` | `hardlink`, `symlink`, `reflink` | `hardlink` | une seule sorte, sans repli ; un échec dit la cause et le réglage à changer |
| `skipRecheck` | booléen | `true` | voir [03-seed-safety.md](03-seed-safety.md) |
| `linkCategory` | chaîne | vide | catégorie qBittorrent des torrents ajoutés ; `NECROMANCIARR_SEED_CATEGORY` l'emporte (espaces retirés) |
| `seedTags` | liste de chaînes | `["necromanciarr"]` | tags posés avec la catégorie sur chaque torrent ajouté. La liste **remplace** le défaut : sans `necromanciarr`, ce tag n'est plus posé (les filtres de qui ou de cross-seed qui le ciblent ne voient plus ces torrents). Espaces retirés, doublons ignorés, liste vide = défaut ; refus d'un tag vide, contenant une virgule ou de plus de 64 caractères, et de plus de 20 tags |
| `language` | `fr`, `en` | `fr` | langue des textes affichés ; casse et espaces ignorés, toute autre valeur (`fr_FR` compris) est un défaut d'usage qui cite la valeur (20 caractères au plus) et les valeurs admises. Appliquée à ce stade : sortie texte, aide et erreurs de la CLI, erreurs de l'API web et descriptions du registre des actions. Viendront ensuite : le diagnostic (`doctor`), les avertissements et le journal du serveur. `config check` écrit la langue effective et sa source sur stderr |
| `partialMaxMissingPercent` | nombre de 0 à 100 | `100` (sans plafond) | part maximale de pièces manquantes d'un `partial_annex` ou `partial_pack` ; au-delà, aucune proposition n'est créée (raison `missing_pieces_above_cap`, voir [01-pipeline.md](01-pipeline.md)). `0` ne garde que les torrents complets |
| `crossSeedConfig` | chemin | vide | relatif : depuis le dossier de configuration |
| `crossSeed` | URL | vide | `http(s)://host:2468?apikey=KEY`, sans identifiants d'utilisateur |
| `crossSeedMode` | `auto`, `delegate`, `native`, `off` | `auto` | casse et espaces ignorés |
| `dedupe` | objet | voir plus bas | |
| `fsProviders` | liste d'objets | vide | voir plus bas |
| `torznab` | liste d'URL ou d'objets | vide | voir « Indexeurs » plus bas |
| `prowlarr` | URL | vide | voir « Indexeurs » plus bas |
| `sourceOrder` | liste de chaînes | `provider`, puis les entrées de `torznab` | voir « Indexeurs » plus bas |
| `trackers` | objet nom → URL ou objet | vide | voir « Annonces des trackers » plus bas |

### Chemins et mappings

Les chemins de la bibliothèque, des dossiers de liens et des racines torrent sont ceux que voit **ce processus**. `pathMappings` traduit vers les autres vues : le client torrent reçoit le dossier du torrent traduit de `from` vers `to` ; les chemins rendus par Radarr et Sonarr sont traduits de `to` vers `from`. Le préfixe le plus long l'emporte, la correspondance se fait sur une frontière de segment (`/data/tv` ne couvre pas `/data/tvshows`), à égalité la première règle gagne. Un côté qui commence par une lettre de lecteur ou `\\` est un chemin Windows : séparateurs `\` ou `/` acceptés, casse ignorée. Sans règle applicable, le service est supposé voir le même chemin, sauf pour un dossier de liens distant : Seed refuse alors (`client_path_unmapped`, voir [05-docker-and-volumes.md](05-docker-and-volumes.md)).

### Dossiers de liens

- Aucun `linkDirs[i]` n'est égal à une racine de bibliothèque, ne la contient ni n'y est contenu : refus au chargement (lexical), puis à l'étape Link sur chemins résolus (liens symboliques suivis).
- Pour chaque torrent, Link choisit le **premier** dossier de `linkDirs` situé sur le même système de fichiers que le premier fichier source. Hardlink et reflink exigent ce même système de fichiers. Sans dossier correspondant, seul un symlink est possible, et seulement si `linkType` vaut `symlink` : sinon la proposition passe `failed` (`no_link_dir_on_same_device`).
- Le dossier du torrent s'appelle `<root name> [<full infohash>]` sous le dossier de liens, droits `0755` pour que le client, souvent sous un autre compte, le lise. Un nom de plus de 255 octets raccourcit le nom racine (sans couper un caractère), jamais l'infohash. Seed et Force utilisent toujours le dossier enregistré pour la proposition (`proposal_steps.link_root`), y compris un dossier `<root name> [<8 characters>]` posé par une version antérieure. Après des liens abîmés, Seed efface l'étape mais garde ce dossier ; Link le reprend seulement s'il se trouve directement sous le dossier de liens choisi pour cette exécution, sinon il crée un nouveau dossier nommé avec l'infohash complet.
- `linkType` est le seul réglage de sorte pour Link : s'il n'est pas accepté par le dossier (sonde réelle), la proposition échoue (`link_kind_refused`) au lieu de changer de sorte.
- Un échec de sorte de lien (`cross_device`, `no_link_dir_on_same_device`, `link_kind_refused`) laisse sur la proposition un avertissement qui nomme la cause et l'action attendue, par exemple « hardlink impossible entre deux montages : placer linkDirs sous le même point de montage que la bibliothèque, ou choisir linkType symlink/reflink ». Après la correction, réaccepter la proposition puis relancer Link.

### Clients torrent, qui, mode lecture seule

- `readonly:` interdit toute écriture : Seed s'arrête (`ErrClientReadOnly`), la déduplication ne peut pas demander de recontrôle (avertissement `recheck_failed`). La lecture (liste, doctor) reste permise.
- **qui** (autobrr) se déclare comme un qBittorrent derrière son proxy : `qbittorrent:http://host:7476/proxy/<key>`, sans identifiants. La clé qui suit `/proxy/` est masquée dans toutes les sorties.
- Le client est appelé sans proxy d'environnement et sans redirection vers un autre hôte ; les corps de réponse sont plafonnés.
- Seul qBittorrent est pris en charge. Un client `deluge`, `transmission` ou `rtorrent` importé de cross-seed est écarté et signalé par rang et type.

## Import du `config.js` de cross-seed

Avec `crossSeedConfig`, `CROSS_SEED_CONFIG` ou `--cross-seed-config`, le fichier est évalué dans un bac à sable JavaScript sans entrée-sortie : ni `require`, ni `import`, ni `eval`, ni `Function` ; seul `process.env.NAME` existe, résolu comme `${NAME}` (environnement réel puis `.env`). Les variables de cet outil (`NECROMANCIARR_*`, `LIBRARY_PROVIDER_*`) y sont toujours absentes (`undefined`, `in` faux) : un `config.js` qui les lit ne reçoit rien, `config check` les nomme sous `crossSeedImport.blockedEnvironment` et `doctor` (contrôle `cross-seed`) avertit, sans jamais en montrer la valeur. Limites : fichier ordinaire de 256 Kio au plus, 2 secondes d'évaluation, 256 entrées par liste. Un chemin relatif de la ligne de commande ou de l'environnement part du dossier courant, celui de `config.json` du dossier de configuration.

| Clé importée | Rôle | Règle de fusion |
|---|---|---|
| `radarr`, `sonarr`, `torrentClients`, `linkDirs` | listes | une liste non vide de `config.json` remplace la liste importée en entier ; `[]` vaut absence |
| `linkType`, `linkCategory` | chaînes | `config.json` si non vide, sinon la valeur importée |
| `skipRecheck` | booléen | `config.json` si présent, sinon la valeur importée |
| `host`, `port`, `apiKey` | démon cross-seed | `crossSeed` explicite l'emporte ; sinon `http://host:port` (`host` vide, `0.0.0.0` ou `::` devient `localhost`, port 2468 par défaut) |

Les autres clés (règles de recherche par indexeurs) sont ignorées ; `config check` liste leurs noms (`ignoredKeys`) sans jamais leur valeur, et indique l'origine de chaque liste (`sources` : `config.json`, `cross-seed` ou `none`). En Docker, le `localhost` du `config.js` désigne le conteneur de cross-seed : renseigner `crossSeed` explicitement.

### Modes cross-seed

| `crossSeedMode` | Comportement effectif |
|---|---|
| `auto` | `delegate` si le démon est configuré et joignable (connexion TCP, 2 s), sinon `native` (avertissement si le démon est configuré mais injoignable) |
| `delegate` | notifie le démon après chaque seed ; sans démon joignable, recherche désactivée avec avertissement, jamais de repli sur `native` |
| `native` | Necromanciarr crée des propositions `cross_seed` pour les autres torrents vérifiés d'un groupe ; le démon est ignoré |
| `off` | aucune recherche de correspondance : un torrent par groupe |

En `delegate`, la notification est `POST /api/webhook` avec l'en-tête `X-Api-Key`, envoyée seulement quand le torrent a été repris à 100 %. Son échec ajoute un avertissement à la proposition, jamais un échec du seed. `status` rend le comportement effectif (`delegate`, `native`, `disabled`). La joignabilité du démon est sondée de nouveau au début de chaque étape, de chaque acceptation (`accept`, `accept-group`) et de chaque `status`, avec un résultat gardé 30 secondes : un `serve` qui tourne longtemps suit l'arrêt et le retour du démon sans redémarrer. La sonde a son propre délai de 2 secondes, indépendant de la requête qui la déclenche ; si cette requête est annulée entre-temps, le résultat n'est pas gardé. Une proposition `cross_seed` ne s'accepte que si le mode effectif du moment est `native` (`a cross_seed proposal can only be accepted in native cross-seed mode`, code `refused`).

## Déduplication (`dedupe`)

Remplace, côté torrent, une copie d'un fichier de la bibliothèque par un lien vers celui-ci. Toujours proposée, jamais automatique ; le fichier de la bibliothèque n'est jamais ouvert en écriture, déplacé ni supprimé.

| Clé | Type | Défaut | Rôle |
|---|---|---|---|
| `torrentRoots` | liste de chemins | vide | chemins absolus sans `..` ; dossiers parcourus. Vide : fonction inactive (`not_configured`). Ne chevauche jamais une racine de bibliothèque |
| `kinds` | liste parmi `symlink`, `hardlink`, `reflink` | `["symlink"]` | sortes essayées dans cet ordre, selon ce que le volume accepte (sonde réelle) |
| `relativeSymlinks` | booléen | `true` | symlink relatif ; si impossible (volumes Windows distincts), erreur, jamais repli sur un chemin absolu |
| `minFileAge` | durée Go ≥ 0 | `10m` | fichiers modifiés plus récemment ignorés (en cours d'écriture) |
| `recheckAfter` | booléen | `true` | demande au client de recontrôler chaque torrent qui contient le fichier (liste lue page par page, voir [06](06-limits-and-troubleshooting.md)) : tous sont mis en pause, recontrôlés, puis seul un torrent complet **qui tournait avant** est repris ; un torrent incomplet, ou que l'utilisateur avait laissé en pause ou arrêté, reste en pause |
| `verify` | `auto`, `full`, `sampled`, `hash` | `auto` | preuve d'identité |

Preuve d'identité : `full` compare tous les octets ; `sampled` compare début, fin et 8 blocs de 1 Mio (une différence ailleurs passe inaperçue) ; `hash` exige l'empreinte du stockage et échoue sans elle ; `auto` utilise l'empreinte du stockage quand les deux fichiers en ont une, sinon lit en entier, jamais par échantillons sans demande. Le plan annonce `bytes_to_read` avant acceptation. Dans tous les cas, `plan` ne retient que les fichiers de même taille et de même empreinte début et fin (1 Mio chacun), et `apply` refait ce contrôle, vérifie que taille et date n'ont pas bougé depuis le plan, crée le lien sous un nom temporaire puis le renomme par-dessus. `dedupe plan` reprend d'abord les entrées temporaires d'une application interrompue.

Sans client torrent configuré (ou avec un client `readonly:`), ou avec `recheckAfter: false`, une proposition `symlink` ou `hardlink` porte l'avertissement `no_client_recheck` (CLI : ligne `warning: no_client_recheck` sous la proposition ; web : « Avertissement ») : le torrent pointera sur le fichier de la bibliothèque sans recontrôle, et un torrent incomplet qui tourne pourrait y écrire. La proposition reste acceptable ; un `reflink` n'est pas concerné (copie à l'écriture).

Un fichier enregistré dans `links` comme `copy` ou `reflink` (copie posée par Force, ou reflink de Link) n'est jamais relié à la bibliothèque : `dedupe plan` le liste avec `actionable: false` et `reason: isolated_copy`, et `dedupe apply` le refuse, pour ne pas défaire l'isolation d'un `partial_annex`.

## Réparation des liens

`repair scan` examine les liens enregistrés, puis les liens inconnus sous `dedupe.torrentRoots` et sous le dossier donné par `--dir` (refusé s'il recouvre la bibliothèque). Pour un lien cassé il cherche la nouvelle cible par inode (disque local seulement), puis taille et date, puis empreinte début-fin (5 lectures au plus par lien), puis pièces du torrent du client. Plusieurs candidats : `ambiguous`, rien n'est choisi. Rien trouvé : `lost`. Seul un lien `repairable` s'accepte. `repair apply` ne remplace un lien que sous `linkDirs`, `dedupe.torrentRoots` ou le dossier du scan, jamais sous la bibliothèque.

Avec un client torrent configuré, chaque lien remplacé fait recontrôler le torrent qui le contient, sous la même garde que dedupe (vue du client par `pathMappings`, pause, recontrôle, reprise seulement à 100 %) : un torrent incomplet reste en pause avec l'avertissement `paused_incomplete_after_recheck`, un torrent laissé en pause par l'utilisateur, ou dans l'état `error` ou `missingFiles`, n'est jamais repris, un recontrôle impossible (aucun torrent ne couvre le lien) donne l'avertissement `recheck_failed`, jamais un échec. Un client déclaré `readonly:` compte comme « sans client ». Sans client, seules les méthodes `inode` et `torrent-pieces`, qui prouvent le contenu, sont applicables : une réparation trouvée par `size-mtime` ou `fingerprint` est listée avec `actionable: false` et `reason: needs_client_recheck` (CLI : ligne `not actionable: needs_client_recheck` ; web : badge « Non applicable sans client torrent »). Pour ces deux méthodes, les torrents qui couvrent le lien sont listés avant le renommage : une lecture en échec (client injoignable, page de plus de 8 Mio, liste qui bouge sans cesse, plus de 200 000 torrents) refuse l'application (`the torrents covering this link cannot be listed, repair refused`), rien n'est remplacé.

## Fournisseurs de système de fichiers (`fsProviders`)

Deux types : `rclone` donne l'empreinte de contenu d'un fichier monté sans le relire ; `sftp` expose un stockage distant par SFTP, désigné par des adresses `sftp://<name>/<absolute path>` dans `libraryRoots`, `linkDirs`, `dedupe.torrentRoots` et `pathMappings[].from`. Chaque entrée est lue selon son type : une clé d'un autre type est refusée en la nommant.

### Type `rclone`

`rclone hashsum`, jamais `--download`.

| Clé | Défaut | Règle |
|---|---|---|
| `type` | obligatoire | `rclone` |
| `name` | obligatoire | `[a-z0-9-]+`, unique |
| `mounts` | vide | `"/chemin/local=remote:sous/chemin"`, un seul `=`, chemin local absolu sans `..`, unique entre fournisseurs. L'imbrication est permise : le plus long préfixe gagne |
| `rc` | vide | URL `http(s)` de l'API `--rc` du processus rclone qui porte le montage ; `user:password@` retiré de l'URL et envoyé en authentification basique ; redirections refusées. Prioritaire sur `binary` |
| `binary` | `rclone` du PATH | exécutable |
| `timeout` | `60s` | durée Go strictement positive |

Les méthodes `rc` appelées forment une liste fermée en lecture seule. Lancer rclone avec `--rc --rc-addr :5572 --rc-user <utilisateur> --rc-pass <motdepasse>` et ne jamais exposer `rc` hors d'un réseau privé : qui l'atteint pilote rclone. Sans `rc` ni binaire, aucune empreinte n'est disponible et la comparaison relit les fichiers.

`dedupe plan` et `dedupe apply` demandent au stockage les algorithmes que ses montages fournissent : la première fois, chaque montage est sondé sur un fichier d'essai (MD5, SHA-1, SHA-256) et le résultat est gardé en base (table `mount_capabilities`, une ligne par montage) ; ensuite seule la base est lue. Une sonde en échec n'est pas gardée. Sans algorithme connu, dedupe demande MD5. Le cache ne se rafraîchit pas seul : après un changement de backend ou de `rc`, supprimer la base ou la ligne du montage. Le contrôle `rclone` de `doctor` sonde toujours de nouveau, sans écrire ce cache.

### Type `sftp`

```json
"fsProviders": [
  {
    "type": "sftp",
    "name": "seedbox",
    "address": "seedbox.example.com:22",
    "user": "media",
    "identityFile": "/data/ssh/id_ed25519",
    "identityPassphrase": "${SFTP_KEY_PASSPHRASE}",
    "knownHosts": "/data/ssh/known_hosts"
  }
],
"libraryRoots": ["sftp://seedbox/srv/media"],
"linkDirs": ["sftp://seedbox/srv/links"]
```

| Clé | Défaut | Règle |
|---|---|---|
| `type` | obligatoire | `sftp` |
| `name` | obligatoire | `[a-z0-9-]+`, unique ; c'est le `<name>` des adresses `sftp://<name>/…` |
| `address` | obligatoire | `host` ou `host:port` (nom d'hôte ou adresse IP), port `22` par défaut ; IPv6 entre crochets (`[::1]` ou `[::1]:22`) ; ni URL (`sftp://…`), ni caractère de contrôle, ni crochet isolé |
| `user` | obligatoire | compte SSH ; un compte sans droit d'écriture sur la bibliothèque est recommandé |
| `knownHosts` | obligatoire | chemin local absolu d'un fichier `known_hosts` OpenSSH ; la clé d'hôte est toujours vérifiée, aucune option ne l'ignore. L'existence du fichier relève de `doctor` |
| `identityFile` | vide | chemin local absolu d'une clé privée (ed25519, ecdsa ou rsa ≥ 3072) ; une clé RSA plus courte est refusée (`identity_key_too_weak`). Les clés de l'agent ne sont pas filtrées |
| `identityPassphrase` | vide | passphrase de `identityFile`, seulement par une référence `${VAR}` ; exige `identityFile` |
| `agent` | `false` | agent SSH (`SSH_AUTH_SOCK`), lu à la connexion seulement |
| `password` | vide | seulement par une référence `${VAR}` ; méthode SSH `password` seulement (pas `keyboard-interactive`) ; méthode la moins recommandée |
| `readBytesPerSecond` | débit global | octets par seconde ; `0` ou absent garde `readBytesPerSecond` global, les deux limites s'appliquent |
| `maxConcurrentRequests` | `16` | requêtes SFTP en vol par fichier, entier de 1 à 256 |
| `timeout` | `30s` | délai d'une opération SFTP, durée Go strictement positive |
| `keepalive` | `15s` | intervalle des requêtes keepalive OpenSSH (trois sans réponse : session morte), durée Go strictement positive |
| `exec` | `auto` | `auto`, `off` ou `required` : commandes distantes en lecture seule (`stat`, `sha1sum`, `md5sum`) |
| `execHashTimeout` | `30m` | délai d'un lot d'empreintes distantes, durée Go strictement positive |

- Au moins une méthode d'authentification : `identityFile`, `agent` ou `password`. Elles sont essayées dans l'ordre agent, clé, mot de passe. Le dossier `~/.ssh` n'est jamais lu implicitement.
- `password` et `identityPassphrase` ne prennent qu'une référence `${VAR}` entière (valeur dans l'environnement ou le `.env`) : une valeur en clair, ou du texte autour de la référence, est une erreur d'usage qui nomme la clé sans citer la valeur. `config check` et toute sortie les masquent ; `identityFile`, `knownHosts`, `address` et `user` restent visibles.
- `mounts`, `rc` et `binary` ne s'appliquent pas au type `sftp` : refusés.
- La base SQLite (`dbPath`) reste sur un disque local.
- Serveurs SFTP Windows (chemins `/C:/…`) : non pris en charge.
- Connexion : `timeout` borne aussi l'ouverture (TCP, SSH, SFTP). Une opération qui dépasse `timeout`, ou trois keepalive sans réponse, ferme toute la connexion : le volume passe `broken` et l'étape s'arrête (`remote_unavailable`). La reconnexion se fait à l'opération suivante, une seule fois par étape.
- Chaque clé de l'agent compte dans le `MaxAuthTries` du serveur : un agent qui porte beaucoup de clés peut épuiser les essais avant la clé de `identityFile` et le mot de passe. Passer `agent: false` ou charger moins de clés. Une clé de l'agent identique à celle de `identityFile` n'est offerte qu'une fois.
- `knownHosts` absent, illisible ou sans entrée valide : refus `known_hosts_unreadable`.
- `agent` sans `SSH_AUTH_SOCK` (ou socket injoignable, ou agent qui ne répond pas dans `timeout`) : la méthode est sautée avec l'avertissement `agent_unavailable`, qui nomme la variable, jamais le chemin de la socket ; `identityFile` lisible par le groupe ou les autres : avertissement `identity_file_permissions`, pas un refus.

#### Débit et limites

- Lecture seule : la bibliothèque distante est lue par des ouvertures `SSH_FXF_READ` seules ; aucune requête d'écriture n'y est envoyée.
- Débit : chaque lecture attend, avant d'être envoyée, le limiteur du fournisseur (`readBytesPerSecond` du fournisseur) puis le limiteur global (`readBytesPerSecond` global) ; le plus strict des deux gouverne. Les octets sont comptés à la demande, par tranches de 32 Kio. Une lecture découpée en `maxConcurrentRequests` requêtes en vol ne transporte jamais plus que ce qui a été compté.
- Listes de dossier : un nom vide, `.`, `..`, contenant `/` ou NUL, ou de plus de 4096 octets est écarté, comme une entrée à la taille impossible (négative ou au-delà de 2^62 octets) ; un nom en double ne garde que sa première entrée. Au-delà d'un million d'entrées, la liste est refusée. Les noms aux octets non UTF-8 restent lisibles.
- Une cible de lien de plus de 4096 octets, une taille de fichier impossible ou une réponse `statvfs` incohérente sont refusées, jamais tronquées.
- Place libre et identité du volume : lues par l'extension OpenSSH `statvfs`. Sans elle, la place libre est inconnue (aucune copie complète n'est tentée) et l'identité du volume aussi.

#### Obtenir la ligne `known_hosts`

L'outil ne fait jamais confiance à une clé d'hôte inconnue et n'écrit jamais dans `knownHosts`. Pour remplir le fichier sans risque :

1. Sur le serveur, par un accès déjà sûr (console, session existante), afficher l'empreinte : `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`.
2. Depuis la machine de l'outil : `ssh-keyscan -H -p 22 seedbox.example.com >> /data/ssh/known_hosts`, puis `ssh-keygen -lf /data/ssh/known_hosts`. Comparer avec l'empreinte de l'étape 1 ; si elles diffèrent, retirer la ligne : la connexion est peut-être interceptée.

Les entrées hachées (`-H`) sont prises en charge. Seuls les types de clé présents pour l'hôte sont demandés au serveur en premier : un serveur qui a aussi une clé d'un autre type n'est pas refusé à tort. Le fichier est relu à chaque connexion.

## Indexeurs (`torznab`, `prowlarr`, `sourceOrder`)

Un indexeur Torznab est une source de secours de `.torrent`, après le fournisseur de bibliothèque. À ce stade `config.json` les accepte et `doctor` les vérifie ; Match ne les interroge pas encore. Chaque entrée de `torznab` est une URL ou un objet :

```json
"torznab": [
  "https://prowlarr.example.com:9696/12/api?apikey=${PROWLARR_API_KEY}",
  {
    "url": "https://prowlarr.example.com:9696/7/api?apikey=${PROWLARR_API_KEY}",
    "name": "tracker-a",
    "tracker": "tracker-a",
    "maxDownloadsPerDay": 20
  }
],
"prowlarr": "https://prowlarr.example.com:9696?apikey=${PROWLARR_API_KEY}",
"sourceOrder": ["provider", "torznab:tracker-a", "torznab:12"]
```

| Clé | Défaut | Validation |
|---|---|---|
| `torznab[].url` | obligatoire | `http(s)`, sans fragment, sans identifiants, `apikey` est le seul paramètre de requête (comme `radarr`) ; secret, masqué partout (`config check`, journaux, `doctor`) ; `${NAME}` fonctionne dedans |
| `torznab[].name` | dernier segment numérique du chemin (`12`), sinon le rang dans la liste | `[a-z0-9-]+`, unique |
| `torznab[].tracker` | vide | nom du tracker tel que le fournisseur l'écrit ; sert au secours `.torrent` et à l'unicité (infohash, tracker) ; gardé tel que saisi (rogné), comparé en minuscules et sans espaces (`Tracker A` vaut `trackera`), comme les clés de `trackers` |
| `torznab[].categories` | celles des capacités de l'indexeur (films, séries) | entiers strictement positifs |
| `torznab[].minInterval` | `3s` | durée Go de `1s` à `1h` : délai minimal entre deux requêtes à cet indexeur (le plancher de `1s` évite qu'il limite ou bannisse l'outil) |
| `torznab[].timeout` | `30s` | durée Go de `1s` à `5m` |
| `torznab[].maxQueriesPerRun`, `maxDownloadsPerRun` | `30`, `10` | entiers de 1 à `100000` |
| `torznab[].maxQueriesPerDay`, `maxDownloadsPerDay` | `200`, `50` | entiers de 1 à `100000` |
| `prowlarr` | vide | URL de l'API native de Prowlarr (`http(s)://host:9696[/base]?apikey=KEY`, mêmes règles que `torznab[].url`) ; seulement pour `doctor` (liste et état des indexeurs), jamais pour chercher |
| `sourceOrder` | `provider`, puis les entrées de `torznab` dans l'ordre de la liste | `provider` doit rester premier (un `provider` mal placé ou absent est refusé, jamais réordonné) ; chaque source est `provider` ou `torznab:<name>` d'un indexeur configuré, citée une fois |

Un indexeur exige `NECROMANCIARR_SECRET_KEY` : sans elle, la configuration est refusée (« torznab exige NECROMANCIARR_SECRET_KEY »), car un `.torrent` d'indexeur porte la passkey du tracker et n'est gardé que scellé ([03](03-seed-safety.md#passkeys-des-torrent-dindexeur)). Générer la clé avec `necromanciarr api-key --secret`.

Un indexeur absent de `sourceOrder` n'est jamais interrogé (`doctor` avertit avec `torznab_not_in_source_order`). Une clé qui contient `&`, `#`, `%` ou `;` doit être encodée en pourcentage dans l'URL (`%26`, `%23`, `%25`, `%3B`) : `?apikey=ab%26cd` vaut `ab&cd`. Une URL refusée reçoit un message générique qui ne la reprend jamais.

Avec Prowlarr, donner le flux Torznab de chaque indexeur (`/<id>/api`, `<id>` étant l'identifiant de l'indexeur dans Prowlarr) et laisser son option `Redirect` désactivée (mode Proxy) : avec une redirection, les liens de téléchargement désignent le tracker d'origine et sont refusés. `doctor` avertit quand une entrée `torznab` ne correspond à aucun indexeur activé de `prowlarr`.

## Annonces des trackers (`trackers`)

Le `.torrent` du fournisseur de bibliothèque n'a pas d'annonce ; pour seeder un torrent privé, qBittorrent a besoin de l'annonce du tracker, qui porte la passkey de l'opérateur. La section `trackers` la donne, pour chaque tracker, afin de l'injecter au seed (hors du dictionnaire `info` : l'infohash ne change pas). `trackers` est indispensable pour seeder les torrents du fournisseur (ils n'ont pas d'annonce). Seed l'injecte en mémoire, juste avant l'ajout au client ; sans annonce, une proposition est `no_announce` ([03-seed-safety.md](03-seed-safety.md)).

```json
"trackers": {
  "tracker-a": "https://tracker-a.example.com/announce/${PASSKEY_TRACKER_A}",
  "tracker-b": {
    "announce": [
      "https://tracker-b.example.com/${PASSKEY_TRACKER_B}/announce",
      "udp://tracker-b.example.com:6969/announce?pk=${PASSKEY_TRACKER_B}"
    ],
    "aliases": ["TrackerB", "tracker-b.example.com"]
  }
}
```

| Règle | Valeur |
|---|---|
| Clé | nom du tracker, comparé au `tracker` d'un candidat et à `torznab[].tracker` en minuscules et sans espaces, puis aux `aliases` sous la même forme. Deux entrées dont une clé ou un alias coïncident sous cette forme sont refusées : le message nomme les deux clés, jamais une URL. Les entrées sont rangées par clé |
| Valeur | une URL, ou un objet `{"announce": [URL…], "aliases": [name…]}` (toute autre clé est refusée, `announce` ne peut pas être vide) ; plusieurs URL forment un seul palier d'`announce-list` (même tracker, plusieurs adresses) |
| URL | `http`, `https` ou `udp`, hôte obligatoire, aucun fragment (`#`, même vide), aucun espace (U+00A0 compris, donc pas de valeur de variable terminée par un espace) ni caractère de contrôle, au plus 2048 octets, au plus 512 URL par entrée ; `${NAME}` interpolé comme ailleurs (variable absente : l'erreur nomme la variable, jamais la valeur) ; une URL refusée reçoit un message générique qui ne la reprend jamais |
| Secret | toute valeur interpolée d'au moins 8 caractères devient un secret connu, masqué partout sous ses formes brute, en capitales, en minuscules, encodée en pourcentage et échappée JSON ; l'URL entière est en plus rendue `scheme://host[:port]/***`. Une variable placée dans l'hôte rend l'hôte secret lui aussi. Une valeur plus courte (un `uid` de quelques chiffres) n'est masquée que dans les URL, comparée à l'identique (après décodage, casse ignorée) à chaque segment de chemin et chaque valeur de paramètre quel qu'en soit le nom, jamais dans le texte libre, où elle couperait des mots ordinaires : `config check` et `doctor` (`tracker_value_too_short`) nomment la variable |
| `config check` | `trackers` liste `key`, `aliases`, `announce` (chaque URL en `https://tracker-a.example.com/***`) et `variables` (noms seulement, par exemple `PASSKEY_TRACKER_A`), jamais une valeur |

Écrire la passkey par `${NAME}` (dans le `.env` ou l'environnement) : une passkey écrite en clair dans `config.json` n'est reconnue comme secret que si c'est un mot d'au moins 16 caractères base64url qui mêle chiffres et lettres. Une passkey qui contient `&`, `#`, `%` ou `;` doit être encodée en pourcentage dans une requête, comme une clé `torznab`.

## Fournisseur de bibliothèque

Contrat HTTP défini dans [`docs/contract/openapi.yaml`](../contract/openapi.yaml), sous la base `libraryApi` : `GET /v1/releases` (filtres `tmdb`, `imdb`, `tvdb`, `q`, `size` répétable 50 fois, `tracker`, `limit` ≤ 100, pagination par curseur), `GET /v1/releases/{infohash}`, `…/torrent`, `…/nfos`. Authentification `Authorization: Bearer <LIBRARY_PROVIDER_TOKEN>`, erreurs `application/problem+json`, `429` avec `Retry-After`. Un fournisseur qui ignore `size` fonctionne : un filtre local de défense écarte les releases sans fichier de la bonne taille, mais il télécharge beaucoup plus de candidats (`doctor` le signale).

## Diagnostic (`doctor`)

`necromanciarr doctor [--check ID]… [--json]`, et côté web `POST /api/doctor/run` (corps `{"checks": […]}`) ou `POST /api/doctor/checks/{id}` : le diagnostic joint les services et écrit des fichiers de sonde dans les dossiers de liens, il n'est donc jamais servi en `GET` (action `doctor` marquée modifiante au registre). Chaque contrôle est borné à 10 secondes et ne rend aucun secret. Le résultat des capacités d'un indexeur Torznab, réussi ou non, est gardé 5 minutes par le serveur (un changement de configuration l'invalide) : relancer `doctor` dans ce délai n'interroge pas de nouveau l'indexeur. Statuts : `ok`, `warn`, `skipped`, `fail`. Le code de sortie vaut 1 s'il y a au moins un `fail`, 0 sinon. Avec une configuration invalide, `doctor` rend un seul contrôle `config` en échec au lieu de s'arrêter.

| Identifiant | Vérifie | `fail` quand |
|---|---|---|
| `config` | chargement, origine des listes (`sources`) | configuration invalide (CLI) |
| `config-dir` | dossier existant, base ouvrable en lecture-écriture | dossier absent, base non ouvrable, base dans la bibliothèque |
| `auth` | écoute loopback, ou jeton présent | hors loopback sans `NECROMANCIARR_AUTH_TOKEN` |
| `library-roots` | racine lisible, un média trouvé dans les 2000 premières entrées | racine absente, non dossier ou illisible ; `warn` si aucune racine ou aucun média trouvé |
| `library-api` | authentification, recherche, filtre `size` | accès refusé, chemin inconnu, indisponible ; `warn` si `size` ignoré |
| `arr:radarr:<rang>`, `arr:sonarr:<rang>` | instance joignable, clé acceptée | injoignable, clé refusée |
| `torznab:<name>` | capacités (`t=caps`, aucune recherche ni téléchargement compté), clé acceptée, un mode de recherche film ou série | clé refusée, injoignable, réponse hostile ; `warn` si ni `movie-search` ni `tv-search` |
| `prowlarr` | API native : liste des indexeurs | clé refusée ; `warn` pour une entrée `torznab` absente ou désactivée dans Prowlarr |
| `qbittorrent` | connexion, liste, mode lecture seule, clients écartés | connexion ou identifiants refusés ; `warn` pour un client non pris en charge |
| `link-feasibility` | sonde réelle des sortes de liens dans chaque dossier de liens et racine torrent ; comparaison de volume avec chaque racine de bibliothèque (sans rien écrire dans la bibliothèque) | aucune sorte voulue acceptée, hardlink ou reflink entre volumes différents ; `warn` si seul un symlink traverse |
| `fs-providers` | mode rc/binaire, empreintes, date de modification, symlinks | fournisseur non constructible ; `warn` avec conseil |
| `cross-seed` | `config.js`, démon, mode effectif | mode invalide ; `warn` si démon injoignable |
| `path-mappings` | chaque règle existe et couvre une racine | `warn` seulement |
| `dedupe` | racines torrent hors bibliothèque | racine qui chevauche la bibliothèque |
