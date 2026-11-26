# Configuration

La configuration vit dans la base SQLite. Seul l'amorçage, ce qu'il faut pour ouvrir la base, reste hors base : options de ligne de commande, variables d'environnement et `.env` du dossier de configuration. Sans base (premier lancement), chaque valeur prend celle du `config.js` de cross-seed ou son défaut. `necromanciarr config check` affiche la configuration effective, secrets masqués, avec l'origine de chaque valeur ; la page Configuration de l'interface, les commandes `config get|set|unset` et `config item set|delete|unset` et les routes `/api/config` la modifient ([04-cli-and-web.md](04-cli-and-web.md)). Chaque processus lit la configuration à son démarrage : après une modification, la réponse porte `restartRequired: true` et `serve` doit être relancé ; une commande ponctuelle suivante la voit déjà.

## Sources et priorité

Du plus fort au plus faible : **options de ligne de commande**, **variables d'environnement**, **base**, **`config.js` de cross-seed** (si un chemin est fourni), **défauts**. Un réglage imposé par une option ou une variable reste modifiable en base, sans effet tant que l'option ou la variable est là : la vue de configuration le marque `locked`, d'origine `env` ou `flag`.

| Réglage | Option | Variable | Clé en base |
|---|---|---|---|
| Dossier de configuration | `--config-dir` | `CONFIG_DIR` | aucune (amorçage) |
| Adresse d'écoute | `--listen` | `NECROMANCIARR_LISTEN` | `listen` |
| Fournisseur de bibliothèque | `--library-api` | `LIBRARY_API` | `libraryApi` |
| Base SQLite | `--db-path` | aucune | aucune (amorçage) |
| Débit de lecture | `--read-bytes-per-second` | aucune | `readBytesPerSecond` |
| Mode cross-seed | `--cross-seed-mode` | `CROSS_SEED_MODE` | `crossSeedMode` |
| `config.js` de cross-seed | `--cross-seed-config` | `CROSS_SEED_CONFIG` | aucune (amorçage) |
| Catégorie des seeds | aucune | `NECROMANCIARR_SEED_CATEGORY` | `linkCategory` |
| Tags des seeds | aucune | `NECROMANCIARR_SEED_TAGS` (séparés par des virgules) | `seedTags` |
| Langue d'affichage | `--language` | `NECROMANCIARR_LANGUAGE` | `language` |
| Plafond de pièces manquantes | aucune | `NECROMANCIARR_PARTIAL_MAX_MISSING_PERCENT` | `partialMaxMissingPercent` |
| Jeton de l'interface | aucune | `NECROMANCIARR_AUTH_TOKEN` | aucune (jamais en base) |
| Clé de scellement | aucune | `NECROMANCIARR_SECRET_KEY` | aucune (jamais en base) |
| Jeton du fournisseur | aucune | `LIBRARY_PROVIDER_TOKEN` | `libraryToken` |

Une variable vide est ignorée. Ces options s'ajoutent à toute sous-commande ; une option inconnue est un défaut d'usage (exit 2).

Le dossier de configuration doit exister quand il est donné. Sans option ni variable, c'est `<user config directory>/necromanciarr` (`~/.config/necromanciarr` sous Linux). L'image Docker fixe `CONFIG_DIR=/data`. La base vaut `necromanciarr.db` par défaut ; un `--db-path` relatif part du dossier de configuration. Elle reste sur un disque local (une adresse `sftp://` est refusée avant toute ouverture) et hors de la bibliothèque (`doctor`). Un chemin relatif de `config.js` part du dossier courant.

### Origines (`config check`)

`config check` écrit sur stdout la configuration effective en JSON, secrets masqués. `origins` donne l'origine de chaque réglage (`key`) et de chaque élément de liste (`key` = liste, `itemId`) : `default`, `crossseed`, `override` (valeur en base qui remplace un défaut ou un élément de cross-seed), `added` (élément ajouté en base), `env`, `flag`, avec `drift: true` pour une surcharge dont l'élément de cross-seed a changé depuis. `orphanOverrides` liste les surcharges sans élément à remplacer (`List`, `ItemID`). La clé `sources` des versions précédentes n'existe plus. La ligne de langue sur stderr donne l'origine : `Language: en (source: flag)`.

## `.env`

- Un seul fichier `.env` est lu : celui du dossier de configuration. S'il n'existe pas, celui du dossier courant est lu à sa place ; `config check` le signale (`workingDirDotEnv: true`) et `doctor` avertit (contrôle `config`). Risque : lancé depuis un dossier qui contient le `.env` d'un autre outil, Necromanciarr en reprendrait les variables (jetons compris) ; placer son `.env` dans le dossier de configuration ferme cette porte. L'environnement réel l'emporte toujours sur `.env`.
- Format : `NAME=value`, une par ligne, commentaires `#`, préfixe `export` toléré, guillemets simples ou doubles retirés. Pas de valeur multiligne, pas d'interpolation dans `.env`.
- Le `.env` sert aux variables de la table ci-dessus et à celles que lit le `config.js` de cross-seed. Aucune valeur en base n'est interpolée : `${NAME}` y est refusé à l'écriture comme au chargement (`${...} references are not supported`, le message nomme la liste, l'élément et le champ, jamais une valeur). Une configuration qui référençait une variable d'environnement reçoit la valeur elle-même, scellée en base si c'est un secret ([Secrets scellés](#secrets-scellés)).

## Valeurs en base

Les réglages sont dans la table `settings` (une valeur JSON par clé, remplacée en entier) ; les listes `radarr`, `sonarr`, `torrentClients`, `linkDirs`, `torznab`, `trackers` et `fsProviders` dans `list_overrides`, élément par élément ([Listes et surcharge par élément](#listes-et-surcharge-par-élément)). Une clé inconnue, une valeur illisible ou un élément invalide bloque le chargement : le message nomme la clé ou l'élément, jamais la valeur, et `doctor` rend le contrôle `config` en échec. L'ancienne section `rclone` est une clé inconnue : elle se déclare dans `fsProviders`.

| Clé | Type | Défaut | Validation |
|---|---|---|---|
| `listen` | `host:port` | `127.0.0.1:8787` | hors boucle locale, `serve` exige `NECROMANCIARR_AUTH_TOKEN` ; les autres commandes non |
| `readBytesPerSecond` | entier (octets/s) | `20971520` (20 Mio/s) | strictement positif |
| `tieBreakReadBytes` | entier (octets) | `53687091200` (50 Gio) | strictement positif. Octets lus au plus, par candidat, pour départager des fichiers locaux de même taille ; la couverture n'est pas concernée |
| `libraryApi` | URL | vide | `http(s)`, sans fragment ; scellée en base. Vide : Match et Verify refusent (`not_configured`) |
| `libraryToken` | texte | vide | jeton Bearer envoyé au fournisseur de bibliothèque ; scellé en base. `LIBRARY_PROVIDER_TOKEN`, quand elle est définie et non vide, l'emporte |
| `libraryRoots` | liste de chemins | vide | vus par ce processus ; chemins absolus, sans segment `..` (refus au chargement). Une adresse `sftp://<provider>/<absolute path>` vise un fournisseur `fsProviders` de type `sftp` déclaré (exemple : `sftp://seedbox/srv/media`) ; un fournisseur `rclone` est refusé. Le serveur n'est joint qu'au premier usage : injoignable, il n'empêche ni le démarrage, ni les opérations locales, ni `doctor` ; Scan laisse alors ses racines telles quelles (`remote_unavailable`) et l'étape suivante retente une fois |
| `pathMappings` | liste `{from, to}` | vide | `from` : vue de ce processus (chemin local, ou adresse `sftp://<provider>/<absolute path>` d'un fournisseur déclaré) ; `to` : vue de Radarr, Sonarr et du client torrent, jamais une adresse `sftp://` |
| `radarr`, `sonarr` | liste d'objets `{url, apiKey, user, password, proxyKey}` | vide | `url` en `http(s)://host:port[/path]`, en clair et sans secret ; `apiKey` obligatoire, `user` et `password` facultatifs (proxy en authentification basique), `proxyKey` pour une clé de proxy dans le chemin (`/proxy/{proxyKey}`). Cross-seed les donne en chaîne `http(s)://[user:password@]host:port[/path]?apikey=KEY`, décomposée de même ; seul `apikey` y est admis |
| `torrentClients` | liste d'objets `{type, readOnly, url, user, password, proxyKey}` | vide | `type` vaut `qbittorrent`, `url` en `http(s)://host:port[/path]` en clair, sans paramètre de requête ; `proxyKey` comme pour `radarr`. Cross-seed les donne en chaîne `qbittorrent[:readonly]:http(s)://[user:password@]host:port[/path]`. Seul le premier client reçoit les torrents (avertissement s'il y en a plusieurs) |
| `linkDirs` | liste d'objets `{path}` | vide | chemins absolus sans `..` ; ne chevauche jamais une racine de `libraryRoots` du même volume |
| `linkType` | `hardlink`, `symlink`, `reflink` | `hardlink` | une seule sorte, sans repli ; un échec dit la cause et le réglage à changer |
| `skipRecheck` | booléen | `true` | voir [03-seed-safety.md](03-seed-safety.md) |
| `linkCategory` | chaîne | vide | catégorie qBittorrent des torrents ajoutés ; `NECROMANCIARR_SEED_CATEGORY` l'emporte (espaces retirés) |
| `seedTags` | liste de chaînes | `["necromanciarr"]` | tags posés avec la catégorie sur chaque torrent ajouté. La liste **remplace** le défaut : sans `necromanciarr`, ce tag n'est plus posé (les filtres de qui ou de cross-seed qui le ciblent ne voient plus ces torrents). Espaces retirés, doublons ignorés, liste vide = défaut ; refus d'un tag vide, contenant une virgule ou de plus de 64 caractères, et de plus de 20 tags |
| `language` | `fr`, `en` | `fr` | langue des textes affichés ; casse et espaces ignorés, toute autre valeur (`fr_FR` compris) est un défaut d'usage qui cite la valeur (20 caractères au plus) et les valeurs admises. Appliquée à ce stade : sortie texte, aide et erreurs de la CLI, erreurs de l'API web et descriptions du registre des actions. Viendront ensuite : le diagnostic (`doctor`), les avertissements et le journal du serveur. `config check` écrit la langue effective et sa source sur stderr |
| `partialMaxMissingPercent` | nombre de 0 à 100 | `100` (sans plafond) | part maximale de pièces manquantes d'un `partial_annex` ou `partial_pack` ; au-delà, aucune proposition n'est créée (raison `missing_pieces_above_cap`, voir [01-pipeline.md](01-pipeline.md)). `0` ne garde que les torrents complets |
| `crossSeed` | URL | vide | `http(s)://host:2468?apikey=KEY`, sans identifiants d'utilisateur ; scellée en base |
| `crossSeedMode` | `auto`, `delegate`, `native`, `off` | `auto` | casse et espaces ignorés |
| `dedupe` | objet | voir plus bas | |
| `fsProviders` | liste d'objets | vide | voir plus bas |
| `torznab` | liste d'objets | vide | voir « Indexeurs » plus bas |
| `prowlarr` | URL | vide | voir « Indexeurs » plus bas |
| `sourceOrder` | liste de chaînes | `provider`, puis les entrées de `torznab` | voir « Indexeurs » plus bas |
| `trackers` | liste d'objets `{key, announce, aliases}` | vide | voir « Annonces des trackers » plus bas |

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
- **qui** (autobrr) se déclare comme un qBittorrent derrière son proxy : `qbittorrent:http://host:7476/proxy/<key>`, sans identifiants. La clé qui suit `/proxy/` va dans `proxyKey`, scellée ; l'`url` garde `/proxy/{proxyKey}` en clair et la clé est masquée dans toutes les sorties.
- Le client est appelé sans proxy d'environnement et sans redirection vers un autre hôte ; les corps de réponse sont plafonnés.
- Seul qBittorrent est pris en charge. Un client `deluge`, `transmission` ou `rtorrent` importé de cross-seed est écarté et signalé par rang et type.

## Listes et surcharge par élément

Cross-seed fournit `radarr`, `sonarr`, `torrentClients` et `linkDirs` ; la base ne garde que ce que l'opérateur remplace, masque ou ajoute, jamais une copie des valeurs de cross-seed. Chaque élément a un identifiant (`itemId`) :

| Liste | Identifiant |
|---|---|
| `radarr`, `sonarr`, `torznab`, `torrentClients` | `scheme://host[:port]` de `url`, en minuscules, sans le port par défaut (80 en http, 443 en https), sans chemin ni identifiants |
| `linkDirs` | `path` |
| `trackers` | `key` |
| `fsProviders` | `name` |

- Deux éléments de cross-seed de même identifiant reçoivent `#2`, `#3`… dans leur ordre (`http://radarr:7878#2`).
- Pour un nouvel élément de ces quatre listes, un identifiant d'une autre forme (chemin, identifiants, requête) est refusé ; l'interface le déduit de `url` tant qu'il n'est pas modifié. Une ligne déjà enregistrée garde le sien.
- Liste effective : les éléments de cross-seed dans leur ordre, chacun remplacé par sa surcharge s'il en a une ou retiré s'il porte un marqueur de suppression, puis les ajouts dans leur ordre d'insertion.
- Une surcharge remplace l'élément entier. Elle suit l'identifiant, pas le rang : elle garde l'identifiant de l'élément visé même quand elle change son `url`, et reste attachée à cet élément si cross-seed réordonne sa liste.
- Une empreinte de l'élément de cross-seed est gardée avec la surcharge : s'il change ensuite, la surcharge reste appliquée et porte `drift: true`.
- Une surcharge ou un marqueur dont l'élément a disparu de cross-seed est orphelin : il n'est pas appliqué, `config check` (`orphanOverrides`) et la page Configuration le signalent, rien n'est supprimé sans demande. Une surcharge illisible laisse l'élément de cross-seed en place et devient orpheline.
- `torznab`, `trackers` et `fsProviders` ne viennent que de la base : chaque élément y est un ajout, sous l'identifiant donné à l'écriture.

## Secrets scellés

Les champs secrets sont scellés en base (AES-256-GCM, clé `NECROMANCIARR_SECRET_KEY`, liés à leur liste, leur élément et leur champ : une valeur scellée recopiée ailleurs ne s'ouvre pas).

| Liste ou réglage | Champs scellés |
|---|---|
| `radarr`, `sonarr` | `apiKey`, `password`, `proxyKey` |
| `torrentClients` | `password`, `proxyKey` |
| `trackers` | `announce` |
| `torznab` | `apiKey` |
| `fsProviders` | `rc`, `password`, `identityPassphrase` |
| réglages | `prowlarr`, `crossSeed`, `libraryApi`, `libraryToken` |

- L'`url` de `radarr`, `sonarr`, `torrentClients` et `torznab` reste en clair, sans secret : une `url` qui porte des identifiants (`user:password@`), un paramètre `apikey` ou une clé après `/proxy/` autre que `{proxyKey}` est refusée à l'écriture comme au chargement (`<list>.url must not carry a secret`), sans que le message reprenne l'URL. Au chargement, `{proxyKey}` reçoit la clé échappée pour un chemin, et l'`apiKey` d'un indexeur rejoint l'URL en `?apikey=`.
- Sans `NECROMANCIARR_SECRET_KEY`, écrire un secret est refusé (`secret_key_missing`, HTTP 409) et rien n'est écrit. Générer la clé avec `necromanciarr api-key --secret`.
- Au chargement, un secret scellé qui ne s'ouvre pas (clé absente ou changée) bloque le démarrage : `sealed config secrets cannot be read: NECROMANCIARR_SECRET_KEY is missing or changed`. Changer de clé impose de ressaisir chaque secret.
- La vue de configuration (`config get`, `GET /api/config`, page Configuration) ne rend jamais un secret : le champ vaut `null`, avec `secret: true`, et `set` dit si une valeur existe. Réécrire un élément sans un de ses secrets garde la valeur enregistrée (ou celle de cross-seed), sauf si sa destination change (`url` ou `proxyKey`, ou `address` d'un `fsProviders`) : l'écriture est alors refusée (`re-enter the secrets when url changes`), pour qu'un secret ne parte jamais vers un autre hôte.

## Import du `config.js` de cross-seed

Avec `CROSS_SEED_CONFIG` ou `--cross-seed-config`, le fichier est évalué dans un bac à sable JavaScript sans entrée-sortie : ni `require`, ni `import`, ni `eval`, ni `Function` ; seul `process.env.NAME` existe, lu dans l'environnement réel puis le `.env`. Les variables de cet outil (`NECROMANCIARR_*`, `LIBRARY_PROVIDER_*`) y sont toujours absentes (`undefined`, `in` faux) : un `config.js` qui les lit ne reçoit rien, `config check` les nomme sous `crossSeedImport.blockedEnvironment` et `doctor` (contrôle `cross-seed`) avertit, sans jamais en montrer la valeur. Limites : fichier ordinaire de 256 Kio au plus, 2 secondes d'évaluation, 256 entrées par liste. Un chemin relatif part du dossier courant. Le fichier est relu à chaque démarrage : ses valeurs ne sont jamais copiées en base.

| Clé importée | Rôle | Règle de fusion |
|---|---|---|
| `radarr`, `sonarr`, `torrentClients`, `linkDirs` | listes | élément par élément ([Listes et surcharge par élément](#listes-et-surcharge-par-élément)) |
| `linkType`, `linkCategory` | chaînes | la base si non vide, sinon la valeur importée |
| `skipRecheck` | booléen | la base si présent, sinon la valeur importée |
| `host`, `port`, `apiKey` | démon cross-seed | `crossSeed` en base l'emporte ; sinon `http://host:port` (`host` vide, `0.0.0.0` ou `::` devient `localhost`, port 2468 par défaut) |

Les autres clés (règles de recherche par indexeurs) sont ignorées ; `config check` liste leurs noms (`ignoredKeys`) sans jamais leur valeur ; l'origine de chaque valeur est dans `origins`. En Docker, le `localhost` du `config.js` désigne le conteneur de cross-seed : enregistrer `crossSeed` en base.

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
| `mounts` | vide | `"/local/path=remote:sub/path"`, un seul `=`, chemin local absolu sans `..`, unique entre fournisseurs. L'imbrication est permise : le plus long préfixe gagne |
| `rc` | vide | URL `http(s)` de l'API `--rc` du processus rclone qui porte le montage ; `user:password@` retiré de l'URL et envoyé en authentification basique ; redirections refusées. Prioritaire sur `binary` |
| `binary` | `rclone` du PATH | exécutable |
| `timeout` | `60s` | durée Go strictement positive |

Les méthodes `rc` appelées forment une liste fermée en lecture seule. Lancer rclone avec `--rc --rc-addr :5572 --rc-user <utilisateur> --rc-pass <motdepasse>` et ne jamais exposer `rc` hors d'un réseau privé : qui l'atteint pilote rclone. Sans `rc` ni binaire, aucune empreinte n'est disponible et la comparaison relit les fichiers.

`dedupe plan` et `dedupe apply` demandent au stockage les algorithmes que ses montages fournissent : la première fois, chaque montage est sondé sur un fichier d'essai (MD5, SHA-1, SHA-256) et le résultat est gardé en base (table `mount_capabilities`, une ligne par montage) ; ensuite seule la base est lue. Une sonde en échec n'est pas gardée. Sans algorithme connu, dedupe demande MD5. Le cache ne se rafraîchit pas seul : après un changement de backend ou de `rc`, supprimer la base ou la ligne du montage. Le contrôle `rclone` de `doctor` sonde toujours de nouveau, sans écrire ce cache.

### Type `sftp`

`seedbox.json`, lu sur l'entrée standard pour que la passphrase ne reste pas dans l'historique du shell :

```json
{
  "type": "sftp",
  "name": "seedbox",
  "address": "seedbox.example.com:22",
  "user": "media",
  "identityFile": "/data/ssh/id_ed25519",
  "identityPassphrase": "dummy-passphrase",
  "knownHosts": "/data/ssh/known_hosts"
}
```

```
necromanciarr config item set fsProviders seedbox --element-stdin < seedbox.json
necromanciarr config set libraryRoots '["sftp://seedbox/srv/media"]'
necromanciarr config item set linkDirs sftp://seedbox/srv/links '{"path": "sftp://seedbox/srv/links"}'
```

| Clé | Défaut | Règle |
|---|---|---|
| `type` | obligatoire | `sftp` |
| `name` | obligatoire | `[a-z0-9-]+`, unique ; c'est le `<name>` des adresses `sftp://<name>/…` |
| `address` | obligatoire | `host` ou `host:port` (nom d'hôte ou adresse IP), port `22` par défaut ; IPv6 entre crochets (`[::1]` ou `[::1]:22`) ; ni URL (`sftp://…`), ni caractère de contrôle, ni crochet isolé, ni caractère de motif `known_hosts` (`,` `*` `?` `!`) ; deux fournisseurs de même adresse (hôte sans casse ni point final, port explicite) partagent leurs racines de bibliothèque ; un nom DNS et son adresse IP ne sont pas rapprochés ; un volume en bouclage et un chemin local peuvent désigner le même disque : non détecté, avertissement `doctor` (`volume_alias_risk`) |
| `user` | obligatoire | compte SSH ; un compte sans droit d'écriture sur la bibliothèque est recommandé |
| `knownHosts` | obligatoire | chemin local absolu d'un fichier `known_hosts` OpenSSH ; la clé d'hôte est toujours vérifiée, aucune option ne l'ignore. L'existence du fichier relève de `doctor` |
| `identityFile` | vide | chemin local absolu d'une clé privée (ed25519, ecdsa ou rsa ≥ 3072) ; une clé RSA plus courte est refusée (`identity_key_too_weak`). Les clés de l'agent ne sont pas filtrées |
| `identityPassphrase` | vide | passphrase de `identityFile`, scellée en base ; exige `identityFile` |
| `agent` | `false` | agent SSH (`SSH_AUTH_SOCK`), lu à la connexion seulement |
| `password` | vide | scellé en base ; méthode SSH `password` seulement (pas `keyboard-interactive`) ; méthode la moins recommandée |
| `readBytesPerSecond` | débit global | octets par seconde ; `0` ou absent garde `readBytesPerSecond` global, les deux limites s'appliquent |
| `maxConcurrentRequests` | `16` | requêtes SFTP en vol par fichier, entier de 1 à 256 |
| `timeout` | `30s` | délai d'une opération SFTP, durée Go strictement positive |
| `keepalive` | `15s` | intervalle des requêtes keepalive OpenSSH (trois sans réponse : session morte), durée Go strictement positive |
| `exec` | `auto` | `auto`, `off` ou `required` : commandes distantes en lecture seule (`stat`, `sha1sum`, `md5sum`) |
| `execHashTimeout` | `30m` | délai d'un lot d'empreintes distantes, durée Go strictement positive |

- Au moins une méthode d'authentification : `identityFile`, `agent` ou `password`. Elles sont essayées dans l'ordre agent, clé, mot de passe. Le dossier `~/.ssh` n'est jamais lu implicitement.
- `password` et `identityPassphrase` se saisissent tels quels et sont scellés à l'écriture ; `config check` et toute sortie les masquent ; `identityFile`, `knownHosts`, `address` et `user` restent visibles.
- `mounts`, `rc` et `binary` ne s'appliquent pas au type `sftp` : refusés.
- La base SQLite (`--db-path`) reste sur un disque local.
- Serveurs SFTP Windows (chemins `/C:/…`) : non pris en charge.
- Connexion : `timeout` borne aussi l'ouverture (TCP, SSH, SFTP). Une opération qui dépasse `timeout`, ou trois keepalive sans réponse, ferme toute la connexion : le volume passe `broken` et l'étape s'arrête (`remote_unavailable`). La reconnexion se fait à l'opération suivante, une seule fois par étape.
- Chaque clé de l'agent compte dans le `MaxAuthTries` du serveur : un agent qui porte beaucoup de clés peut épuiser les essais avant la clé de `identityFile` et le mot de passe. Passer `agent: false` ou charger moins de clés. Une clé de l'agent identique à celle de `identityFile` n'est offerte qu'une fois.
- `knownHosts` absent, illisible ou sans entrée valide : refus `known_hosts_unreadable`.
- `agent` sans `SSH_AUTH_SOCK` (ou socket injoignable, ou agent qui ne répond pas dans `timeout`) : la méthode est sautée avec l'avertissement `agent_unavailable`, qui nomme la variable, jamais le chemin de la socket ; `identityFile` lisible par le groupe ou les autres : avertissement `identity_file_permissions`, pas un refus.

#### Débit et limites

- Lecture seule : la bibliothèque distante est lue par des ouvertures `SSH_FXF_READ` seules ; aucune requête d'écriture n'y est envoyée.
- Débit : chaque lecture attend, avant d'être envoyée, le limiteur du fournisseur (`readBytesPerSecond` du fournisseur) puis le limiteur global (`readBytesPerSecond` global) ; le plus strict des deux gouverne. Les octets sont comptés à la demande, par tranches de 32 Kio. Une lecture découpée en `maxConcurrentRequests` requêtes en vol ne transporte jamais plus que ce qui a été compté.
- Listes de dossier : un nom vide, `.`, `..`, contenant `/` ou NUL, ou de plus de 4096 octets est écarté, comme une entrée à la taille impossible (négative ou au-delà de 2^62 octets) ; un nom en double ne garde que sa première entrée. Au-delà d'un million d'entrées, la liste est refusée. La liste reçue est gardée entière en mémoire avant ce contrôle : une liste sans fin n'est arrêtée que par `timeout`, et peut occuper environ débit × `timeout` × 10 octets de mémoire (une entrée décodée pèse environ dix fois sa taille sur le réseau). Les noms aux octets non UTF-8 restent lisibles.
- Une lecture qui s'arrête avant la taille du fichier (fichier qui grandit encore, serveur qui coupe la lecture, connexion qui lâche) est rendue indisponible (`remote_unavailable`), jamais comme une fin de fichier : le fichier n'est pas jugé différent.
- Une cible de lien de plus de 4096 octets, une taille de fichier impossible ou une réponse `statvfs` incohérente sont refusées, jamais tronquées.
- Place libre et identité du volume : lues par l'extension OpenSSH `statvfs`. Sans elle, la place libre est inconnue (aucune copie complète n'est tentée) et l'identité du volume aussi. L'identité du volume vient du `statvfs` du serveur : un serveur qui ment ne fausse que les choix de l'examen et des liens (même volume ou non), jamais une écriture dans la bibliothèque.

#### Écriture distante

- Les liens se posent dans un dossier de liens du même serveur (par exemple `sftp://seedbox/srv/links`), hors de toute racine de bibliothèque ; chaque écriture passe par la racine d'écriture confinée de ce dossier.
- Extensions OpenSSH utilisées : `hardlink` pour un lien dur, `posix-rename` pour un remplacement atomique. Sans `hardlink`, le lien dur est refusé comme une sorte non prise en charge. Sans `posix-rename`, aucun remplacement n'est fait (`no_atomic_replace`) : jamais un renommage simple ni une suppression puis création. Le lien symbolique n'en exige aucune : sa cible est envoyée telle quelle et le serveur la lit depuis le dossier du lien. À chaque session, une sonde crée puis retire un lien d'essai dans le dossier de liens, pour vérifier l'ordre des arguments ; un serveur qui les lit dans l'ordre inverse (lien posé à la place de la cible) voit ses liens symboliques refusés.
- Force (copie isolée) est refusé sur un volume SFTP (`server_copy_unavailable`) : le client n'offre pas de copie côté serveur (`copy-data`), et une copie par flux ferait transiter tout le média par le réseau ([03-seed-safety.md](03-seed-safety.md)).
- Sondes : avant de compter sur une fonction, le volume l'essaie dans le premier dossier de liens, jamais dans une bibliothèque : lien symbolique (ordre des arguments), lien dur, remplacement atomique (`posix-rename`), `statvfs`. Une fonction n'est retenue que si le serveur annonce l'extension **et** que l'essai réussit ; sinon sa raison est `extension_missing` (non annoncée, aucune requête envoyée) ou `probe_failed` (annoncée, essai en échec). La copie côté serveur vaut toujours `server_copy_unsupported_by_client`. Les fichiers d'essai portent un nom `.necromanciarr-probe-` suivi de 32 caractères hexadécimaux, sont retirés dans tous les cas, et les restes d'une sonde interrompue sont retirés à la sonde suivante (seuls ces noms exacts, jamais un dossier, jamais en profondeur). Le résultat vaut pour la session ; celui enregistré en base (`volume_capabilities`) n'est qu'un indice d'affichage, la session suivante sonde à nouveau. Avant la première sonde, aucune de ces fonctions n'est annoncée (`not_probed`).
- Confinement : un chemin écrit est relatif au dossier de liens, sans `..`, sans segment vide ni NUL. Chaque dossier parent est vérifié (un lien symbolique est refusé), et sa résolution par le serveur (`realpath`) doit rester son propre chemin, avant et après l'écriture, la suppression ou le remplacement ; une entrée créée sous un parent détourné est signalée (`ErrUnconfined`), jamais retirée : la retirer suivrait le même parent détourné. SFTP n'a pas d'ouverture relative à un dossier : entre le dernier contrôle et l'écriture, la suppression ou le remplacement, un acteur qui écrit déjà dans le dossier de liens distant peut encore les détourner. Ce risque est accepté ; la défense recommandée est un compte SFTP qui n'écrit que dans le dossier de liens et lit seulement la bibliothèque ([05-docker-and-volumes.md](05-docker-and-volumes.md)).
- Un dossier créé modifiable par le groupe ou les autres (umask du compte) donne l'avertissement `link_dir_world_writable`, sans échec ; les droits demandés ne sont jamais posés par chemin.
- Une session perdue invalide la racine d'écriture : l'opération rend `remote_unavailable`, et l'étape suivante rouvre et revérifie la racine.

#### Exécution distante en lecture seule

SFTP ne donne ni inode, ni nombre de liens, ni empreinte calculée par le serveur. Quand `exec` le permet, le volume lance sur la même connexion SSH, dans des sessions à part, une liste fermée de commandes en lecture : `stat` (périphérique, inode, liens, taille, date) et `sha1sum`/`md5sum` (empreinte complète calculée par l'hôte). Aucune écriture, aucun lien, aucun renommage ne passe par exec : les écritures restent SFTP, dans la racine confinée.

- Liste fermée : les lignes envoyées sont des constantes, construites une fois. Aucun nom de fichier n'y figure : les chemins passent sur l'entrée standard, séparés par NUL, lus par `xargs -0`, après `--`. Les sorties sont des enregistrements terminés par NUL (`--printf=…\0`, `-z`) : un nom qui contient un saut de ligne ne décale rien.
- Lignes possibles (`<TAB>` est une tabulation) :

```
printf %s 'a'\''b\' '!x'
'stat' '--version'
'xargs' '--version'
'sha1sum' '--version'
'md5sum' '--version'
'nice' '--version'
'ionice' '--version'
'xargs' '-0' '-r' 'stat' '--printf=%d<TAB>%i<TAB>%h<TAB>%s<TAB>%Y\0' '--'
'xargs' '-0' '-r' 'sha1sum' '-z' '--'
'xargs' '-0' '-r' 'md5sum' '-z' '--'
'nice' '-n' '10' 'ionice' '-c3' 'xargs' '-0' '-r' 'sha1sum' '-z' '--'
'nice' '-n' '10' 'ionice' '-c3' 'xargs' '-0' '-r' 'md5sum' '-z' '--'
```

- Shell de connexion POSIX requis : le serveur fait lire chaque ligne au shell de connexion du compte (`$SHELL -c`). La première ligne de chaque session est une sonde constante : sa sortie doit valoir exactement `a'b\!x`. Sous fish ou csh, elle diffère : toute exécution distante est coupée pour la session (`login_shell_not_posix`), y compris les lignes constantes, et le volume continue en SFTP seul. Une ligne de sonde qui ne finit pas (délai, sortie démesurée) donne `exec_probe_failed` : SFTP seul pour l'étape, sonde refaite à l'étape suivante.
- Sonde : ensuite, `stat`, `sha1sum` et `md5sum` doivent être ceux de GNU coreutils, `xargs` celui de GNU findutils ; `nice` et `ionice` présents ajoutent le préfixe `nice -n 10 ionice -c3` aux empreintes, pour ne pas écraser le disque de l'hôte. Sans `xargs` GNU, chaque fichier part dans sa propre commande `stat --printf=… -- '/…'` : le chemin, absolu, est alors le seul argument externe, entre guillemets simples POSIX. Le verdict vaut pour la session.
- Chemins admis : absolus, canoniques, sans NUL, d'au plus 4096 octets, sous une racine de bibliothèque, un dossier de liens ou une racine torrent (`dedupe.torrentRoots`) du même volume ; sinon refus avant tout envoi.
- Bornes : 64 fichiers par commande (une liste plus longue est découpée), une commande à la fois par volume ; sortie standard plafonnée à 64 Kio pour `stat` et à 64 × (longueur du chemin + 128) octets pour les empreintes ; sortie d'erreur lue sur 4 Kio au plus et jamais recopiée (elle peut citer des chemins) : seuls le code de sortie et une raison typée remontent. Délai de 30 s par lot `stat`, `execHashTimeout` par lot d'empreintes ; au dépassement, la session reçoit `KILL` puis est fermée (un serveur OpenSSH antérieur à 7.9 ignore le signal : la fermeture du canal reste).
- Contrôles : chaque enregistrement `stat` a exactement cinq champs numériques, et sa taille doit égaler celle que donne SFTP pour le même chemin ; chaque empreinte (hexadécimal minuscule) doit être suivie du chemin envoyé, octet pour octet, dans l'ordre d'envoi. Un enregistrement manquant, en trop ou mal formé rejette tout le lot : aucun résultat partiel.
- `exec: auto` : sonde au premier usage ; exec refusé par le serveur (`exec_denied`) ou sonde en échec : SFTP seul, sans erreur. `exec: off` : jamais d'exec. `exec: required` : exec indisponible est une erreur.
- Durcissement côté serveur (recommandé, non exigé) : une clé dédiée dans `authorized_keys`, avec `restrict` et une commande forcée de garde (`restrict,command="<guard>" ssh-ed25519 …`). La garde compare `SSH_ORIGINAL_COMMAND` octet pour octet aux lignes ci-dessus, lance une ligne reconnue par `/bin/sh -c` et refuse tout le reste ; elle laisse passer le sous-système SFTP. Les lignes « un fichier par commande » ne passent pas une telle garde : un hôte gardé doit avoir `xargs` GNU. Avec un compte qui ne peut que lire la bibliothèque, exec ne peut rien y écrire même si un appel échappait au code.

#### Obtenir la ligne `known_hosts`

L'outil ne fait jamais confiance de lui-même à une clé d'hôte inconnue : seule la commande `sftp trust <name> --yes`, lancée par l'opérateur, ajoute une ligne à `knownHosts` ([04-cli-and-web.md](04-cli-and-web.md)). Pour remplir le fichier sans risque :

1. Sur le serveur, par un accès déjà sûr (console, session existante), afficher l'empreinte : `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`.
2. Avec l'outil : `necromanciarr sftp trust seedbox` affiche l'empreinte présentée sans rien écrire ; si elle égale celle de l'étape 1, relancer avec `--yes`. Dans Docker, lancer la commande dans le conteneur, avec le dossier de `knownHosts` monté en écriture le temps de cet ajout ([05-docker-and-volumes.md](05-docker-and-volumes.md)).
3. Ou sans l'outil, depuis sa machine : `ssh-keyscan -H -p 22 seedbox.example.com >> /data/ssh/known_hosts`, puis `ssh-keygen -lf /data/ssh/known_hosts`. Comparer avec l'empreinte de l'étape 1 ; si elles diffèrent, retirer la ligne : la connexion est peut-être interceptée.

Les entrées hachées (`-H`) sont prises en charge. Seuls les types de clé présents pour l'hôte sont demandés au serveur en premier : un serveur qui a aussi une clé d'un autre type n'est pas refusé à tort. Le fichier est relu à chaque connexion.

## Indexeurs (`torznab`, `prowlarr`, `sourceOrder`)

Un indexeur Torznab est une source de secours de `.torrent`, après le fournisseur de bibliothèque. Chaque élément de `torznab` est un objet ; `tracker-a.json` :

```json
{
  "url": "https://prowlarr.example.com:9696/7/api",
  "apiKey": "dummy-api-key",
  "name": "tracker-a",
  "tracker": "tracker-a",
  "maxDownloadsPerDay": 20
}
```

```
necromanciarr config item set torznab tracker-a --element-stdin < tracker-a.json
necromanciarr config set prowlarr --value-stdin < prowlarr.json
necromanciarr config set sourceOrder '["provider", "torznab:tracker-a"]'
```

`prowlarr.json` contient la chaîne JSON `"https://prowlarr.example.com:9696?apikey=dummy-api-key"`.

| Clé | Défaut | Validation |
|---|---|---|
| `torznab[].url` | obligatoire | `http(s)`, sans fragment, sans identifiants ni paramètre de requête ; en clair |
| `torznab[].apiKey` | obligatoire | clé de l'indexeur, ajoutée à l'URL en `apikey` ; secret, scellé en base et masqué partout (`config check`, journaux, `doctor`) |
| `torznab[].name` | obligatoire | `[a-z0-9-]+`, unique ; un élément sans `name` bloque le chargement (`a "name" is required`) |
| `torznab[].tracker` | vide | nom du tracker tel que le fournisseur l'écrit ; sert au secours `.torrent` et à l'unicité (infohash, tracker) ; gardé tel que saisi (rogné), comparé en minuscules et sans espaces (`Tracker A` vaut `trackera`), comme les clés de `trackers` |
| `torznab[].categories` | celles des capacités de l'indexeur (films, séries) | entiers strictement positifs |
| `torznab[].minInterval` | `3s` | durée Go de `1s` à `1h` : délai minimal entre deux requêtes à cet indexeur (le plancher de `1s` évite qu'il limite ou bannisse l'outil) |
| `torznab[].timeout` | `30s` | durée Go de `1s` à `5m` |
| `torznab[].maxQueriesPerRun`, `maxDownloadsPerRun` | `30`, `10` | entiers de 1 à `100000` |
| `torznab[].maxQueriesPerDay`, `maxDownloadsPerDay` | `200`, `50` | entiers de 1 à `100000` |
| `prowlarr` | vide | URL de l'API native de Prowlarr (`http(s)://host:9696[/base]?apikey=KEY`, mêmes règles que `torznab[].url`, scellée en base) ; seulement pour `doctor` (liste et état des indexeurs), jamais pour chercher |
| `sourceOrder` | `provider`, puis les entrées de `torznab` dans l'ordre de la liste | `provider` doit rester premier (un `provider` mal placé ou absent est refusé, jamais réordonné) ; chaque source est `provider` ou `torznab:<name>` d'un indexeur configuré, citée une fois |

Un indexeur exige `NECROMANCIARR_SECRET_KEY` : sans elle, la configuration est refusée (« torznab exige NECROMANCIARR_SECRET_KEY »), car un `.torrent` d'indexeur porte la passkey du tracker et n'est gardé que scellé ([03](03-seed-safety.md#passkeys-des-torrent-dindexeur)). Générer la clé avec `necromanciarr api-key --secret`.

Un indexeur absent de `sourceOrder` n'est jamais interrogé (`doctor` avertit avec `torznab_not_in_source_order`). La clé `torznab[].apiKey` se saisit telle quelle ; dans `prowlarr`, une clé qui contient `&`, `#`, `%` ou `;` doit être encodée en pourcentage dans l'URL (`%26`, `%23`, `%25`, `%3B`) : `?apikey=ab%26cd` vaut `ab&cd`. Une URL refusée reçoit un message générique qui ne la reprend jamais.

Avec Prowlarr, donner le flux Torznab de chaque indexeur (`/<id>/api`, `<id>` étant l'identifiant de l'indexeur dans Prowlarr) et laisser son option `Redirect` désactivée (mode Proxy) : avec une redirection, les liens de téléchargement désignent le tracker d'origine et sont refusés. `doctor` avertit quand une entrée `torznab` ne correspond à aucun indexeur activé de `prowlarr`.

## Annonces des trackers (`trackers`)

Le `.torrent` du fournisseur de bibliothèque n'a pas d'annonce ; pour seeder un torrent privé, qBittorrent a besoin de l'annonce du tracker, qui porte la passkey de l'opérateur. La section `trackers` la donne, pour chaque tracker, afin de l'injecter au seed (hors du dictionnaire `info` : l'infohash ne change pas). `trackers` est indispensable pour seeder les torrents du fournisseur (ils n'ont pas d'annonce). Seed l'injecte en mémoire, juste avant l'ajout au client ; sans annonce, une proposition est `no_announce` ([03-seed-safety.md](03-seed-safety.md)).

Deux éléments, l'un à une annonce, l'autre à plusieurs adresses et alias (passkeys factices) :

```json
{"key": "tracker-a", "announce": "https://tracker-a.example.com/announce/dummypasskey0001"}
```

```json
{
  "key": "tracker-b",
  "announce": [
    "https://tracker-b.example.com/dummypasskey0002/announce",
    "udp://tracker-b.example.com:6969/announce?pk=dummypasskey0002"
  ],
  "aliases": ["TrackerB", "tracker-b.example.com"]
}
```

```
necromanciarr config item set trackers tracker-b --element-stdin < tracker-b.json
```

| Règle | Valeur |
|---|---|
| Clé | `key`, nom du tracker, comparé au `tracker` d'un candidat et à `torznab[].tracker` en minuscules et sans espaces, puis aux `aliases` sous la même forme. Deux entrées dont une clé ou un alias coïncident sous cette forme sont refusées : le message nomme les deux clés, jamais une URL. Les entrées sont rangées par clé |
| Valeur | `announce` : une URL ou une liste d'URL, jamais vide ; `aliases` : liste de noms, facultative ; toute autre clé est refusée ; plusieurs URL forment un seul palier d'`announce-list` (même tracker, plusieurs adresses) |
| URL | `http`, `https` ou `udp`, hôte obligatoire, aucun fragment (`#`, même vide), aucun espace (U+00A0 compris) ni caractère de contrôle, au plus 2048 octets, au plus 512 URL par entrée ; une URL refusée reçoit un message générique qui ne la reprend jamais |
| Secret | `announce` est scellé en base. Au chargement, chaque segment de chemin et chaque valeur de paramètre décodés d'une annonce deviennent des secrets connus, sauf un mot ordinaire en minuscules, en capitales ou à capitale initiale (`announce`, `Announce`, `tracker-b`) : d'au moins 8 caractères, masqués partout sous leurs formes brute, en capitales, en minuscules, encodée en pourcentage et échappée JSON ; plus courts (un `uid` de quelques chiffres), masqués seulement dans les URL, comparés à l'identique (après décodage, casse ignorée), jamais dans le texte libre, où ils couperaient des mots ordinaires. L'URL entière est en plus rendue `scheme://host[:port]/***` |
| `config check` | `trackers` liste `key`, `aliases`, `announce` (chaque URL en `https://tracker-a.example.com/***`), jamais une passkey |

Une passkey qui contient `&`, `#`, `%` ou `;` doit être encodée en pourcentage dans une requête, comme une clé `torznab`.

## Fournisseur de bibliothèque

Contrat HTTP défini dans [`docs/contract/openapi.yaml`](../contract/openapi.yaml), sous la base `libraryApi` : `GET /v1/releases` (filtres `tmdb`, `imdb`, `tvdb`, `q`, `size` répétable 50 fois, `tracker`, `limit` ≤ 100, pagination par curseur), `GET /v1/releases/{infohash}`, `…/torrent`, `…/nfos`. Authentification `Authorization: Bearer <token>`, erreurs `application/problem+json`, `429` avec `Retry-After`. Un fournisseur qui ignore `size` fonctionne : un filtre local de défense écarte les releases sans fichier de la bonne taille, mais il télécharge beaucoup plus de candidats (`doctor` le signale).

## Diagnostic (`doctor`)

`necromanciarr doctor [--check ID]… [--json]`, et côté web `POST /api/doctor/run` (corps `{"checks": […]}`) ou `POST /api/doctor/checks/{id}` : le diagnostic joint les services et écrit des fichiers de sonde dans les dossiers de liens, il n'est donc jamais servi en `GET` (action `doctor` marquée modifiante au registre). Chaque contrôle est borné à 10 secondes et ne rend aucun secret. Le résultat des capacités d'un indexeur Torznab, réussi ou non, est gardé 5 minutes par le serveur (un changement de configuration l'invalide) : relancer `doctor` dans ce délai n'interroge pas de nouveau l'indexeur. Statuts : `ok`, `warn`, `skipped`, `fail`. Le code de sortie vaut 1 s'il y a au moins un `fail`, 0 sinon. Avec une configuration invalide, `doctor` rend un seul contrôle `config` en échec au lieu de s'arrêter.

| Identifiant | Vérifie | `fail` quand |
|---|---|---|
| `config` | chargement, base comprise ; origine de chaque liste (`none`, `cross-seed`, `database`) | configuration invalide, en base comprise (réglage inconnu, valeur ou élément illisible, secret scellé qui ne s'ouvre pas) |
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
