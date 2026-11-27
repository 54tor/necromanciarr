# Ligne de commande

```text
necromanciarr <commande> [arguments] [options]
```

Les arguments positionnels viennent **avant** les options (`--nom valeur` ou `--nom=valeur`). Une ligne invalide affiche l'usage et sort avec le code 2 ; une erreur d'exécution sort avec le code 1. Les options de [configuration](/configuration) (`--config-dir`, `--listen`, `--language`, …) s'appliquent à toutes les commandes. Chaque action de l'interface web a sa commande.

## Pipeline

| Commande | Effet |
| --- | --- |
| `scan`, `match`, `verify`, `link`, `seed` | lance une étape du pipeline |
| `status` | candidats par état, propositions par décision, étapes d'application |
| `proposals list` | liste les propositions (`--kind`, `--state`, `--group`, `--source`, `--current-only`) |
| `proposals accept <id>` | accepte une proposition de torrent |
| `proposals accept-group <groupe>` | accepte la principale du groupe |
| `proposals reject <id>`, `proposals choose <id>` | rejette ; fait d'une proposition la principale de son groupe |
| `proposals force <id> --yes` | démarre un torrent incomplet en pause, après copie indépendante des médias exposés |
| `links list` | liste les liens enregistrés et leur état, en lecture seule |

Rien n'est lié, ajouté au client ou modifié sans proposition acceptée.

## Maintenance

| Commande | Effet |
| --- | --- |
| `dedupe plan`, `dedupe apply <id>` | remplace les copies côté torrent par des liens |
| `repair scan`, `repair apply <id>` | propose la nouvelle cible des liens cassés, puis l'applique |
| `doctor` | diagnostic de la configuration, des services et des dossiers (`--check ID`) |
| `sources list`, `sources test <nom>` | chaîne de sources et test d'un indexeur Torznab |
| `volumes list`, `volumes probe <nom>`, `volumes commands <nom>` | volumes locaux nommés et sftp : état, sonde des liens, lignes que la garde distante doit admettre |
| `sftp trust <nom> --yes` | ajoute la clé d'hôte d'un fournisseur sftp à `knownHosts` (CLI seulement) |

## Service et configuration

| Commande | Effet |
| --- | --- |
| `serve` | sert l'interface et l'API jusqu'à SIGINT ou SIGTERM ; génère au premier lancement la clé de scellement et le jeton dans le `.env` du dossier de configuration |
| `healthcheck` | sonde `/healthz` sur la boucle locale (sonde Docker) |
| `api-key` | affiche un jeton aléatoire neuf, sans rien écrire ; `--secret` : une clé de scellement |
| `config check` | configuration effective, secrets masqués, avec l'origine de chaque valeur |
| `config get [clé]` | la configuration en base, entière ou une clé |
| `config set <clé> <json>`, `config unset <clé>` | enregistre ou retire un réglage (`--value-stdin` garde un secret hors de l'historique) |
| `config item set\|delete\|unset <liste> <id>` | surcharge, masque ou ajoute un élément de liste (`--element-stdin`) |

```bash
necromanciarr config set linkType '"symlink"'
necromanciarr doctor
```
