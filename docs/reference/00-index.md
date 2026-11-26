# Référence Necromanciarr

Necromanciarr retrouve, pour des fichiers média déjà présents dans une bibliothèque (Plex, Radarr, Sonarr), les torrents dont ils sont issus. Il vérifie le contenu pièce par pièce, reconstitue l'arborescence du torrent par des liens vers la bibliothèque, puis ajoute le torrent à qBittorrent sans retéléchargement. Il ne modifie jamais un fichier de la bibliothèque : il ne fait que créer des liens à côté.

Cette référence décrit le comportement du code livré. Elle est tenue à jour avec lui ; le site (`docs/site`) en est la présentation publique.

## Plan

| Page | Contenu |
|---|---|
| [01-pipeline.md](01-pipeline.md) | Les cinq étapes, les états, les propositions, les générations, la vérification, la reprise |
| [02-configuration.md](02-configuration.md) | configuration en base, amorçage (`.env`, variables d'environnement, options), priorités, surcharge par élément de cross-seed, secrets scellés, diagnostic |
| [03-seed-safety.md](03-seed-safety.md) | Les invariants qui protègent la bibliothèque et le ratio |
| [04-cli-and-web.md](04-cli-and-web.md) | Commandes, codes de sortie, serveur web, authentification, routes, interface |
| [05-docker-and-volumes.md](05-docker-and-volumes.md) | Image, volumes, droits, exemple de déploiement |
| [06-limits-and-troubleshooting.md](06-limits-and-troubleshooting.md) | Limites connues, risques acceptés en v1, dépannage |

Le contrat du fournisseur de bibliothèque est dans [`docs/contract/openapi.yaml`](../contract/openapi.yaml).

## Vocabulaire

| Terme | Sens |
|---|---|
| Bibliothèque | Dossiers (`libraryRoots`) qui contiennent les médias. Lus, jamais écrits. |
| Fichier média | Fichier d'extension `mkv mp4 avi m4v ts m2ts wmv mpg mpeg mov flac mp3 m4a aac ogg opus`. |
| Fournisseur de bibliothèque | Service HTTP qui sert les releases, leurs `.torrent` et leurs `.nfo` (`libraryApi`). |
| Candidat | Une release du fournisseur dont au moins un fichier a exactement la taille d'un fichier local. Identifié par (infohash, tracker). |
| Association | Lien entre un fichier du torrent et un fichier local, d'abord par taille, puis prouvé par hash. |
| Annexe | Fichier du torrent qui n'est pas un média (`.nfo`, sample, sous-titre, image). Absent de la bibliothèque la plupart du temps. |
| Pièce | Bloc du torrent contrôlé par un SHA1. Une pièce « à cheval » couvre la fin d'un fichier et le début du suivant. |
| Proposition | Résultat que l'opérateur doit décider : accepter, rejeter ou choisir. Elle a une nature (`kind`) et une décision (`decision`). |
| Génération | Compteur d'un candidat, incrémenté à chaque invalidation. Une proposition ne vaut que pour la génération où elle est née. |
| Périmé (`stale`) | État d'un candidat dont un fichier local a changé, disparu ou dont l'association a changé. |
| Groupe | Ensemble de candidats qui se disputent un même fichier local (même épisode chez deux trackers, épisode seul et pack). |
| Principale | La proposition `full` d'un groupe que `accept-group` accepte. Élue automatiquement ou choisie par l'opérateur. |
| Litige (`conflict`) | Proposition qui liste plusieurs torrents possibles pour un même fichier. Elle ne s'accepte pas : on choisit un torrent. |
| Dossier de liens | Dossier de `linkDirs` où Link reconstitue chaque torrent. |
| Racine torrent | Dossier de `dedupe.torrentRoots` que la déduplication parcourt. |
| Lien | Hardlink, symlink ou reflink créé par l'outil et enregistré en base. |
| Verrou d'étape | Verrou unique en base : une seule étape, action `dedupe` ou action `repair` tourne à la fois. |
| Job | Action longue lancée par l'API web ; une à la fois, 50 conservés en mémoire. |
| Force | Démarrage explicite d'un `partial_annex` laissé en pause. |
| cross-seed | Démon externe qui cherche d'autres trackers pour un torrent ; Necromanciarr le lit (`config.js`) et le prévient, ou le remplace (mode `native`). |

## Conventions

- Un identifiant de proposition, de groupe ou de job est un entier strictement positif.
- Les clés de configuration, la vue de configuration (`config get`, `/api/config`) et `config check` sont en `camelCase`, les autres champs JSON des sorties CLI et API en `snake_case`.
- Les sorties `--json` et l'API ne contiennent aucun secret : jetons, clés d'API, mots de passe, clé du chemin `/proxy/` de qui et identifiants rc y sont masqués par `***` ou `[redacted]`.
- Les textes destinés à l'opérateur (messages d'erreur, motifs de refus, textes de `doctor`) suivent le réglage `language` ; les codes, noms de champs JSON, commandes et options restent en anglais, et quelques textes ne sont pas encore traduits ([04-cli-and-web.md](04-cli-and-web.md)). Les pages de cette référence existent en français et en anglais ([en/00-index.md](en/00-index.md)).
- Les exemples utilisent des valeurs fictives : `/home/u/…`, `/data/…`, `example.com`, `alice`.
- Une durée de configuration est une durée Go (`10m`, `90s`, `1h`).
