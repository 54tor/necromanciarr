# Configuration

## Sources et priorité

De la plus forte à la plus faible :

1. Options de ligne de commande : `--listen`, `--library-api`, `--db-path`, `--read-bytes-per-second`.
2. Variables d'environnement (un `.env` ne surcharge jamais l'environnement réel).
3. `config.json` dans le dossier de configuration.
4. Valeurs par défaut.

Le dossier de configuration se détermine par `--config-dir`, puis `CONFIG_DIR`, puis `<dossier utilisateur>/necromanciarr`.
Un dossier choisi explicitement doit exister.

Le fichier est lu en mode strict : une clé inconnue, ou du texte après l'objet JSON, est refusé.

## Secrets et interpolation

Toute chaîne de `config.json` accepte `${VAR}`, remplacé par la variable d'environnement
ou par une entrée du `.env`. Le `.env` est lu dans le dossier de configuration puis dans le dossier courant.

| Variable | Rôle |
| --- | --- |
| `NECROMANCIARR_AUTH_TOKEN` | Jeton de l'interface. Obligatoire si `listen` n'est pas une adresse de boucle locale |
| `LIBRARY_PROVIDER_TOKEN` | Jeton Bearer envoyé au fournisseur de bibliothèque ([contrat](/api)) |
| `LIBRARY_API` | URL de base du fournisseur, équivalent de `libraryApi` |

## Champs de `config.json`

```json
{
  "listen": "127.0.0.1:8787",
  "dbPath": "/data/necromanciarr.db",
  "readBytesPerSecond": 20971520,
  "libraryApi": "https://library.example.com",
  "libraryRoots": ["/home/u/media/movies", "/home/u/media/tv"],
  "pathMappings": [{ "from": "/home/u/media", "to": "/data/media" }],
  "radarr": ["http://example.com:7878?apikey=${RADARR_API_KEY}"],
  "sonarr": ["http://example.com:8989?apikey=${SONARR_API_KEY}"],
  "torrentClients": ["qbittorrent:readonly:http://alice:${QBIT_PASSWORD}@example.com:8080"],
  "dedupe": {
    "torrentRoots": [],
    "kinds": ["symlink"],
    "relativeSymlinks": true,
    "minFileAge": "10m",
    "recheckAfter": true
  }
}
```

| Champ | Défaut | Rôle |
| --- | --- | --- |
| `listen` | `127.0.0.1:8787` | Adresse d'écoute. Hors boucle locale, `NECROMANCIARR_AUTH_TOKEN` est exigé |
| `dbPath` | `necromanciarr.db` | Base SQLite |
| `readBytesPerSecond` | 20 Mo/s | Plafond de lecture disque ; doit être positif |
| `libraryApi` | vide | URL `http(s)` du fournisseur de bibliothèque, sans fragment |
| `libraryRoots` | vide | Racines de la bibliothèque Plex |
| `pathMappings` | vide | Traductions de préfixes `from` → `to` entre hôte, conteneur et client torrent |
| `radarr`, `sonarr` | vide | Instances, `http://hote:port?apikey=…` ; la clé est extraite de l'URL |
| `torrentClients` | vide | `type[:readonly]:http://utilisateur:motdepasse@hote:port` ; seul `qbittorrent` existe |
| `dedupe.torrentRoots` | vide | Dossiers des torrents à relier. Ne doivent ni contenir ni être contenus dans une racine de bibliothèque |
| `dedupe.kinds` | `["symlink"]` | Types de lien : `symlink`, `hardlink`, `reflink` |
| `dedupe.relativeSymlinks` | `true` | Liens symboliques relatifs |
| `dedupe.minFileAge` | `10m` | Âge minimal d'un fichier avant remplacement (durée Go, non négative) |
| `dedupe.recheckAfter` | `true` | Redemande une vérification au client après remplacement |
| `dedupe.verify` | `auto` | Mode de preuve d'identité : `auto`, `full`, `sampled`, `hash` |

### Stockage distant : `fsProviders`

Pour lire les empreintes d'un stockage distant, Necromanciarr s'appuie sur des fournisseurs de système de fichiers,
déclarés dans la liste `fsProviders`. rclone est le seul type disponible aujourd'hui ; d'autres types sont prévus.
L'ancienne section `rclone` n'existe plus : elle est refusée au chargement, avec un message qui renvoie vers `fsProviders`.

```json
"fsProviders": [
  {
    "type": "rclone",
    "name": "mega",
    "rc": "http://user:${RCLONE_RC_PASS}@rclone:5572",
    "timeout": "60s",
    "mounts": ["/volumes/mega=megas4:bucket/prefix"]
  }
]
```

Clés communes à tous les types :

| Champ | Rôle |
| --- | --- |
| `type` | Obligatoire. Seule valeur connue : `rclone` (un type inconnu est refusé, avec la liste des types connus) |
| `name` | Obligatoire, unique, `^[a-z0-9-]+$` |
| `mounts` | Entrées `cheminLocal=cible` : chemin absolu, sans doublon exact entre fournisseurs ; l'imbrication est permise et le montage le plus long l'emporte |

Clés du type `rclone` (une clé inconnue est refusée en la nommant) :

| Champ | Rôle |
| --- | --- |
| `rc` | URL de l'API `--rc` du processus rclone qui porte le montage (identifiants masqués à l'affichage) |
| `binary` | Chemin d'un binaire rclone, secours pour les installations natives |
| `timeout` | Durée Go strictement positive |

Pour le type `rclone`, la cible d'un montage s'écrit `remote:sousChemin`.
Sans `rc` ni `binary`, aucune empreinte distante n'est demandée.
