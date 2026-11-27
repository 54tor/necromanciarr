# Configuration

La configuration vit dans la base SQLite. Elle se modifie par la page Configuration de l'interface,
par la commande `config` ou par les routes `/api/config`, et s'applique au prochain démarrage de `serve`.

## Sources et priorité

De la plus forte à la plus faible :

1. Options de ligne de commande : `--listen`, `--library-api`, `--read-bytes-per-second`, `--cross-seed-mode`, `--language`.
2. Variables d'environnement (un `.env` ne surcharge jamais l'environnement réel).
3. La base.
4. Le `config.js` de cross-seed, si `--cross-seed-config` ou `CROSS_SEED_CONFIG` le désigne.
5. Valeurs par défaut.

Une valeur imposée par une option ou une variable s'affiche verrouillée dans la page Configuration.

## Amorçage

Ce qu'il faut pour ouvrir la base reste hors base.

| Option ou variable | Rôle |
| --- | --- |
| `--config-dir`, `CONFIG_DIR` | Dossier de configuration (`<dossier utilisateur>/necromanciarr` par défaut). Un dossier choisi explicitement doit exister |
| `--db-path` | Base SQLite, `necromanciarr.db` du dossier de configuration par défaut ; toujours sur un disque local |
| `--cross-seed-config`, `CROSS_SEED_CONFIG` | Chemin du `config.js` de cross-seed |
| `NECROMANCIARR_SECRET_KEY` | Clé qui scelle les secrets en base ; sans elle, saisir un secret est refusé. Générée au premier `serve` |
| `NECROMANCIARR_AUTH_TOKEN` | Jeton qui crée le compte au premier lancement, puis refusé. Obligatoire si `listen` n'est pas une adresse de boucle locale ; généré au premier `serve` |
| `LIBRARY_PROVIDER_TOKEN` | Jeton Bearer envoyé au fournisseur de bibliothèque ([contrat](/api)) ; facultatif, le réglage `libraryToken` de la base sert sinon |

Le `.env` est lu dans le dossier de configuration, à défaut dans le dossier courant.

### Premier démarrage

`serve` ajoute au `.env` du dossier de configuration la clé de scellement et le jeton qui manquent (fichier créé au mode `0600`, valeur déjà présente jamais modifiée). Le jeton neuf est affiché une seule fois sur stderr ; il reste lisible dans le `.env` et dans les journaux du conteneur. Pour le renouveler, supprimer la ligne `NECROMANCIARR_AUTH_TOKEN` du `.env` et redémarrer.

`serve` refuse de démarrer si une base existe alors que `NECROMANCIARR_SECRET_KEY` manque (une clé neuve rendrait les secrets scellés illisibles : restaurer la clé d'origine), et si le dossier n'est pas inscriptible (le rendre modifiable ou définir les deux variables, `api-key` et `api-key --secret` en tirent les valeurs).

L'interface ouvre ensuite l'assistant (onglet Assistant) tant que Radarr, Sonarr et les clients torrent sont tous vides : il teste Radarr, Sonarr et qBittorrent avant d'enregistrer, puis demande un redémarrage. Il passe par `POST /api/setup/test` ; le diagnostic propose de créer un dossier de liens absent par `POST /api/setup/fix/create_link_dir`.

### Compte

L'interface s'ouvre avec un compte unique (identifiant, mot de passe d'au moins 12 caractères), créé au premier lancement avec le jeton ci-dessus ; le jeton est ensuite refusé. Un second facteur est facultatif : clé de sécurité WebAuthn, code TOTP (qui exige `NECROMANCIARR_SECRET_KEY`), ou clé d'accès pour se connecter sans mot de passe. Dès qu'un second facteur existe, le mot de passe seul ne suffit plus. Après un échec, le limiteur impose 1 s d'attente, doublée à chaque échec jusqu'à 60 s, sans verrouillage définitif ; un attaquant qui insiste sans relâche bloque toutefois les nouvelles connexions tant que l'attaque dure (les sessions ouvertes continuent) : le bloquer au proxy. Une session dure 14 jours au plus et expire après 24 h d'inactivité. Les clés WebAuthn exigent `publicUrl` en https (ou `localhost`), avec un nom d'hôte et non une adresse IP.

Accès perdu : `necromanciarr account reset --yes` efface le compte et ses facteurs, et le jeton rouvre la création.

## Cross-seed et surcharges

Les instances Radarr et Sonarr, les clients torrent et les dossiers de liens du `config.js` servent de valeurs
par défaut, relues à chaque démarrage. La base ne garde que les éléments que l'opérateur remplace, masque ou
ajoute : changer l'URL d'une instance ne détache pas les autres de cross-seed. Un élément s'identifie par son URL
sans identifiants (`http://radarr:7878`), son chemin, sa clé de tracker ou son nom de fournisseur.

```sh
necromanciarr config get radarr
necromanciarr config item set radarr http://radarr:7878 --element-stdin < radarr.json
necromanciarr config item delete radarr http://radarr:7878   # masque l'élément de cross-seed
necromanciarr config item unset radarr http://radarr:7878    # retire la surcharge
necromanciarr config set linkType '"symlink"'
```

`radarr.json` : `{"url": "http://radarr:7878", "apiKey": "dummy-api-key"}`.

## Secrets

Clés d'API, clés de proxy, mots de passe, passkeys d'annonce et `rc` rclone sont scellés en base (AES-256-GCM)
et ne sont jamais rendus : la page Configuration affiche « Défini » ou « Non défini ». Les URL des services et des
indexeurs restent en clair, sans secret (`{proxyKey}` marque une clé de proxy dans le chemin). Passer un secret par
`--value-stdin` ou `--element-stdin` le garde hors de l'historique du shell. Aucune valeur en base n'est
interpolée : `${VAR}` y est refusé.

## Réglages

| Clé | Défaut | Rôle |
| --- | --- | --- |
| `listen` | `127.0.0.1:8787` | Adresse d'écoute. Hors boucle locale, `NECROMANCIARR_AUTH_TOKEN` est exigé |
| `publicUrl` | vide | Adresse `http(s)://host[:port]` que les utilisateurs atteignent. Fixe l'origine WebAuthn (https, ou `localhost`), les cookies `Secure` et l'`Origin` admis ; à régler derrière un proxy HTTPS |
| `readBytesPerSecond` | 20 Mo/s | Plafond de lecture disque ; doit être positif |
| `libraryApi` | vide | URL `http(s)` du fournisseur de bibliothèque, sans fragment |
| `libraryRoots` | vide | Racines de la bibliothèque Plex |
| `pathMappings` | vide | Traductions de préfixes `from` → `to` entre hôte, conteneur et client torrent |
| `dedupe.torrentRoots` | vide | Dossiers des torrents à relier. Ne doivent ni contenir ni être contenus dans une racine de bibliothèque |
| `dedupe.kinds` | `["symlink"]` | Types de lien : `symlink`, `hardlink`, `reflink` |
| `dedupe.relativeSymlinks` | `true` | Liens symboliques relatifs |
| `dedupe.minFileAge` | `10m` | Âge minimal d'un fichier avant remplacement (durée Go, non négative) |
| `dedupe.recheckAfter` | `true` | Redemande une vérification au client après remplacement |
| `dedupe.verify` | `auto` | Mode de preuve d'identité : `auto`, `full`, `sampled`, `hash` |

`dedupe` s'enregistre en un seul objet : `config set dedupe '{"kinds": ["symlink"], "minFileAge": "10m"}'`.

| Liste | Élément |
| --- | --- |
| `radarr`, `sonarr` | `{url, apiKey, user, password}` |
| `torrentClients` | `{type, readOnly, url, user, password}` ; seul `qbittorrent` existe |
| `linkDirs` | `{path}` |
| `trackers` | `{key, announce, aliases}` |
| `torznab` | objet d'indexeur, `name` obligatoire |
| `fsProviders` | voir ci-dessous |

### Indexeurs : `torznab`, `prowlarr`, `sourceOrder`

Un indexeur Torznab est une source de secours de `.torrent`, interrogée après le fournisseur de bibliothèque. Avec Prowlarr, donnez le flux Torznab de chaque indexeur (`https://prowlarr.example.com:9696/7/api`, `7` étant son identifiant dans Prowlarr) dans `url`, sa clé dans `apiKey` et un `name` en `[a-z0-9-]+` ; la clé est scellée en base, ce qui exige `NECROMANCIARR_SECRET_KEY`. L'assistant de premier démarrage propose cette étape (facultative) et teste l'indexeur en ne lisant que ses capacités.

Le réglage `prowlarr` (`http(s)://host:9696[/base]?apikey=KEY`) ne sert qu'au diagnostic, qui compare vos indexeurs à ceux de Prowlarr ; il n'est jamais utilisé pour chercher. Sans `sourceOrder`, la chaîne est le fournisseur puis tous les indexeurs `torznab` : un nouvel indexeur est donc interrogé tout de suite. Un `sourceOrder` enregistré doit commencer par `provider` et citer chaque indexeur en `torznab:<name>` ; un indexeur absent de cette liste n'est jamais interrogé.

### Stockage distant : `fsProviders`

Pour lire les empreintes d'un stockage distant, Necromanciarr s'appuie sur des fournisseurs de système de fichiers,
déclarés dans la liste `fsProviders` : `rclone` (empreintes d'un montage), `sftp` (stockage distant, voir la
référence) et `local` (volume nommé du disque local, sondé par `volumes probe`). Exemple d'élément `rclone`, enregistré par
`necromanciarr config item set fsProviders mega --element-stdin < mega.json` :

```json
{
  "type": "rclone",
  "name": "mega",
  "rc": "http://user:dummy-password@rclone:5572",
  "timeout": "60s",
  "mounts": ["/volumes/mega=megas4:bucket/prefix"]
}
```

Clés communes à tous les types :

| Champ | Rôle |
| --- | --- |
| `type` | Obligatoire : `rclone`, `sftp` ou `local` (un type inconnu est refusé, avec la liste des types connus) |
| `name` | Obligatoire, unique, `^[a-z0-9-]+$` |

Clés du type `rclone` (une clé inconnue est refusée en la nommant) :

| Champ | Rôle |
| --- | --- |
| `mounts` | Entrées `cheminLocal=remote:sousChemin` : chemin absolu, sans doublon exact entre fournisseurs ; l'imbrication est permise et le montage le plus long l'emporte |
| `rc` | URL de l'API `--rc` du processus rclone qui porte le montage ; scellée en base |
| `binary` | Chemin d'un binaire rclone, secours pour les installations natives |
| `timeout` | Durée Go strictement positive |

Sans `rc` ni `binary`, aucune empreinte distante n'est demandée.

Clés du type `local` : `paths`, liste non vide de dossiers absolus, sans segment `..`. `necromanciarr volumes probe <nom>` mesure alors, pour de vrai, hardlink, symlink et reflink dans le premier dossier de `linkDirs` posé sous un de ces dossiers, sans laisser de fichier.

```json
{ "type": "local", "name": "nas", "paths": ["/media/library", "/media/links"] }
```
