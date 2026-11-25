# Commandes et interface web

La CLI et le web appellent la même couche (`app.Service`). Toute action web a sa commande, toute commande d'action a sa route et son bouton : un test échoue si une route n'a pas d'action, une action pas de commande, ou une action pas de bouton.

## Commandes

Syntaxe : `necromanciarr <command> [arguments] [options]`. Les arguments positionnels viennent **avant** les options. Options de valeur : `--name value` ou `--name=value`. Toute option que la commande ne connaît pas est transmise à la configuration (`--config-dir`, `--db-path`, `--listen`, `--language`, `--library-api`, `--read-bytes-per-second`, `--cross-seed-config`, `--cross-seed-mode`), qui refuse ce qu'elle ne connaît pas.

| Commande | Effet | Options |
|---|---|---|
| `scan`, `match`, `verify`, `link`, `seed` | lance une étape ([01-pipeline.md](01-pipeline.md)) | |
| `status` | candidats par état, propositions par décision, étapes d'application, mode cross-seed effectif, torrents vérifiés non proposés (`held`, une ligne `held:` par torrent avec la raison) | |
| `proposals list` | liste les propositions | `--kind K`, `--state S`, `--group G`, `--source S`, `--current-only` |
| `proposals accept <id>` | accepte une proposition de torrent | `--allow-partial-pack` |
| `proposals accept-group <groupe>` | accepte la principale du groupe (et les `cross_seed` en mode `native`) | |
| `proposals reject <id>` | rejette (depuis `pending` seulement) | |
| `proposals choose <id>` | fait d'une proposition `full` la principale de son groupe | |
| `proposals force <id>` | démarre un `partial_annex` en pause ([03-seed-safety.md](03-seed-safety.md)) | `--yes` obligatoire |
| `dedupe plan` | propose des liens pour les copies côté torrent | |
| `dedupe apply <id>` ou `--all-accepted` | accepte (si en attente) et applique une proposition ; ou applique, une à une, toutes les acceptées | `--dry-run`, `--all-accepted` |
| `repair scan` | propose la nouvelle cible des liens cassés | `--dir D` |
| `repair apply <id>` ou `--all-accepted` | idem `dedupe apply` | `--dry-run`, `--all-accepted` |
| `links list` | liste les liens enregistrés avec leur état courant, en lecture seule | `--state S`, `--limit N`, `--offset N` |
| `doctor` | diagnostic ([02-configuration.md](02-configuration.md)) | `--check ID` (répétable) |
| `sources list` | la chaîne de sources dans son ordre, sans requête réseau | |
| `sources test <name>` | teste un indexeur : `t=caps` et une recherche vide, au quota du jour | |
| `serve` | sert l'interface et l'API jusqu'à SIGINT ou SIGTERM | |
| `healthcheck` | sonde `/healthz` sur la boucle locale | |
| `api-key` | affiche un jeton aléatoire neuf ; `--secret` : une clé de scellement | |
| `config check` | affiche la configuration effective, secrets masqués | |

Valeurs : `--kind` parmi `full partial_annex partial_pack cross_seed conflict dedupe repair` ; `--state` d'une proposition parmi `pending accepted rejected applied failed superseded` ; `--state` d'un lien parmi `ok diverged lost unreadable` ; `--limit` vaut 200 par défaut et 1000 au plus, `--offset` part de 0 ; `--source` vaut `provider` ou `torznab:<name>`. Une valeur inconnue est un défaut d'usage (exit 2), jamais une liste vide.

Chaque proposition porte `details.source`, la source qui a fourni son candidat (`provider` ou `torznab:<name>`, jamais une URL) ; `--source` (paramètre `source` du web) ne garde que les propositions de cette source.

`sources list` rend une ligne par source dans l'ordre de la chaîne (`sourceOrder`, le fournisseur toujours premier), puis les indexeurs configurés hors de `sourceOrder` (`order` 0) : `order`, `source`, `type` (`provider` ou `torznab`), `tracker` (le `torznab[].tracker` déclaré ; une valeur en forme d'URL n'est rendue que `scheme://host/***`), `state`, `queries` et `downloads` (`{"used", "cap"}` : usage sur 24 h glissantes face à `maxQueriesPerDay` et `maxDownloadsPerDay` ; `null` pour le fournisseur, sans quota), `caps_cached` (vrai quand les capacités de l'indexeur sont dans le cache de 24 h du client de ce processus ; faux sinon, ou tant qu'une requête `t=caps` est en cours) et `last_error`. `state` vaut `ok`, `blocked` (un `Retry-After` reçu écarte l'indexeur), `exhausted` (un plafond du jour est atteint) ou `disabled` (hors de la chaîne : sans `libraryApi`, ou absent de `sourceOrder`). `last_error` est le code de la dernière requête en échec de l'indexeur, vide après une réussite : `indexer_unavailable` (injoignable, délai dépassé, statut d'erreur), `indexer_access_denied`, `indexer_hostile_response` (réponse hostile ou illisible), `indexer_budget_exhausted`, `foreign_download_host` ; jamais le texte de l'erreur ni une URL. `indexer_budget_exhausted` y vient aussi du plafond par exécution atteint pendant la dernière exécution. La CLI affiche le libellé traduit entre parenthèses. L'état `blocked` et `last_error` vivent dans le processus qui a fait les requêtes (le serveur pour l'interface) ; une commande CLI, processus neuf, ne voit que l'usage et les plafonds, lus en base.

`sources test <name>` accepte `torznab:<name>` ou le seul nom de l'indexeur. Il passe par le budget comme Match : chaque requête est comptée au quota du jour, un indexeur écarté ou à son plafond n'est pas joint. Des capacités encore en cache dans le processus (24 h) ne sont pas redemandées. L'intervalle `minInterval` ne tient que dans un processus : un test lancé par la CLI a son propre client, qui ne voit pas les requêtes du serveur ; les plafonds du jour, eux, restent atomiques entre processus. Un second test du même indexeur pendant qu'un premier tourne dans le même processus est refusé (`refused`). Résultat : `{"source", "result": "ok"|"failed", "error"}`, `error` portant un des codes de `last_error` ; un échec du test sort en code 1. Le fournisseur ne se teste pas ici (`refused`, voir `doctor --check library-api`), un nom inconnu est un `not_found`, un indexeur hors de la chaîne un `not_configured`.

`dedupe apply <id>` et `repair apply <id>` exigent la bonne nature (`wrong proposal kind`) et une proposition applicable (`ambiguous` et `lost` ne le sont pas). Sans `--dry-run`, une proposition en attente est acceptée puis appliquée ; avec `--dry-run`, tous les contrôles tournent sans rien créer. `--all-accepted` n'applique que les propositions déjà acceptées de la nature, continue après un échec et se combine avec `--dry-run`.

### Codes de sortie

| Code | Sens |
|---|---|
| 0 | réussi ; les avertissements sont une ligne `warning: …` par avertissement sur stderr (une étape qui a des éléments en échec sort 0 avec `N item(s) failed`) |
| 1 | échec : étape déjà en cours, composant non configuré, examen des liens en cours, erreur d'entrée-sortie, au moins un `fail` de `doctor`, `healthcheck` indisponible |
| 2 | usage : commande, option ou identifiant invalide, ou confirmation manquante (`proposals force` sans `--yes`) |
| 3 | proposition périmée (`stale_proposal`) |
| 4 | action refusée par une règle (litige, pack partiel sans option, proposition non acceptée, hors racines, client en lecture seule…) |

Avec `--all-accepted` : 3 si une proposition était périmée, sinon 1 si une a échoué.

### Sortie `--json`

`--json` est accepté par toutes les commandes d'action et de lecture (pas par `serve`, `healthcheck`, `api-key`, `config check` qui a sa sortie JSON propre). stdout porte le JSON indenté, trié par clé, secrets masqués ; les avertissements y figurent déjà et ne sont pas répétés sur stderr. Les listes vides sont `[]`, jamais `null`. Une erreur est `{"error": {"code": "...", "message": "..."}}` sur stdout, avec le même code de sortie. Les champs sont en `snake_case`.

`proposals accept|reject|choose` répondent `{"proposal_id", "action"}` ; `accept-group` `{"group_id", "accepted": [...]}` ; `dedupe apply` et `repair apply` un objet `{proposal_id, applied, dry_run, link_target, bytes_read, warnings}`, ou un tableau de ces objets (avec `error` par élément en échec) pour `--all-accepted`.

### `serve`, `healthcheck`, `api-key`

- `serve` écrit la ligne de journal `server_listening` (`listening on http://<address>`, traduite) sur stdout et, hors boucle locale, la ligne `non_loopback_warning` (`listening outside loopback, every api request needs the bearer token`, traduite) sur stderr. `serve` refuse de démarrer hors boucle locale sans `NECROMANCIARR_AUTH_TOKEN`, avant d'ouvrir la base ; les autres commandes, `healthcheck` compris, chargent la même configuration sans jeton : `listen "0.0.0.0:8787" is not loopback: set NECROMANCIARR_AUTH_TOKEN (at least 32 characters, generate one with necromanciarr api-key)`. Arrêt propre à SIGINT/SIGTERM : requêtes en cours terminées sous 10 s, jobs annulés.
- `healthcheck` charge la même configuration, interroge `GET /healthz` sur l'hôte d'écoute (`0.0.0.0`, `::` ou hôte vide sont sondés par `127.0.0.1`), 3 s au plus, sans proxy ni redirection. Il affiche `ok` (exit 0, identique dans les deux langues) ou la ligne `healthcheck_failed` (`healthcheck: unavailable`, traduite, exit 1) et rien d'autre.
- `api-key` tire 256 bits d'aléa, les affiche en base64url sans remplissage (43 caractères) sur stdout et rappelle sur stderr de les mettre dans `.env`. Rien n'est écrit sur disque ni journalisé. `api-key --secret` fait de même pour `NECROMANCIARR_SECRET_KEY` (32 octets, voir [03](03-seed-safety.md#passkeys-des-torrent-dindexeur)).

## Serveur web

### Authentification

`NECROMANCIARR_AUTH_TOKEN` (32 caractères au moins) est la seule source du jeton : `necromanciarr api-key` en génère un à recopier dans `.env`.

| Écoute | Jeton | Contrôle de `Host` |
|---|---|---|
| boucle locale (`127.0.0.1`, `[::1]`, `localhost`) | facultatif ; sans jeton, l'API est ouverte à tout processus local | oui |
| autre adresse (`0.0.0.0`, IP du réseau) | **obligatoire** au démarrage de `serve` (pas pour les commandes ponctuelles) | non : le jeton protège quel que soit le nom, derrière un mandataire notamment |

- Le jeton ne se lit que dans `Authorization: Bearer <token>` (schéma insensible à la casse), jamais dans l'URL ni un cookie. Comparaison des empreintes SHA-256 en temps constant. Un jeton refusé : 401, `WWW-Authenticate: Bearer`, une ligne de journal `api: unauthorized request from <address>`, sans limite de débit.
- Un jeton défini sur une écoute en boucle locale est exigé aussi.
- `Host` doit être l'adresse d'écoute, `localhost`, `127.0.0.1` ou `[::1]` sur le même port (anti DNS rebinding) ; sinon 403 `unknown host header`.
- `Sec-Fetch-Site: cross-site` : 403 `cross-site request refused`, toutes méthodes.
- `POST` : `Content-Type: application/json` obligatoire (415 sinon) ; si `Origin` est présent il doit être celui de la requête (403 sinon) ; sans `Origin` et sans jeton configuré, `X-Requested-With: necromanciarr` est exigé. Corps de 64 Kio au plus (413), JSON strict : champ inconnu ou texte après l'objet donne 400 `invalid json body` ; un corps vide vaut un objet vide.
- Aucun en-tête CORS n'est posé. Toute réponse porte `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, `Cross-Origin-Opener-Policy: same-origin` et une CSP `default-src 'self'` (ni script, ni style, ni ressource inline ou externe). L'API ajoute `Cache-Control: no-store`.
- `GET /healthz` est public, sans contrôle : `200 ok` ou `503 unavailable` selon la réponse de la base (2 s). Il ne rend ni version ni configuration.
- Délais du serveur : en-têtes 10 s, lecture 30 s, inactivité 120 s, en-têtes de 16 Kio au plus.

### Routes

| Méthode | Chemin | Action | Corps / paramètres | Réponse |
|---|---|---|---|---|
| GET | `/api/status` | `status` | | 200 |
| GET | `/api/proposals` | `proposals list` | `decision`, `kind`, `group`, `current_only`, `source` | 200, tableau ; chaque proposition porte `updated_at` (RFC 3339 UTC : le plus récent de sa création, de sa décision et de sa dernière étape d'application ; vide si inconnu) |
| POST | `/api/proposals/{id}/accept` | `proposals accept` | `{"allow_partial_pack": bool}` | 200 |
| POST | `/api/proposals/{id}/reject` | `proposals reject` | | 200 |
| POST | `/api/proposals/{id}/choose` | `proposals choose` | `{"candidate_id": n}` (le candidat de la proposition ou un membre de son litige) | 200 |
| POST | `/api/proposals/{id}/force` | `proposals force` | `{"confirm": true}` | 200 |
| POST | `/api/groups/{id}/accept` | `proposals accept-group` | | 200 |
| POST | `/api/stages/{stage}/run` | `run` | `stage` parmi `scan match verify link seed` | 202, job |
| POST | `/api/dedupe/plan` | `dedupe plan` | | 202, job |
| POST | `/api/dedupe/{id}/apply` | `dedupe apply` | `{"dry_run": bool, "accept_pending": bool}` | 202, job |
| POST | `/api/repair/scan` | `repair scan` | `{"directory": "..."}` | 202, job |
| POST | `/api/repair/{id}/apply` | `repair apply` | `{"dry_run": bool, "accept_pending": bool}` | 202, job |
| GET | `/api/links` | `links list` | `state`, `limit`, `offset` | 200, page |
| POST | `/api/doctor/run` | `doctor` | `{"checks": ["config", …]}` (vide : tous) | 200, rapport |
| POST | `/api/doctor/checks/{checkId}` | `doctor` | `{}` | 200, rapport |
| GET | `/api/sources` | `sources list` | | 200, tableau |
| POST | `/api/sources/{name}/test` | `sources test` | `{}` | 200, résultat du test |
| GET | `/api/jobs`, `/api/jobs/{id}` | suivi des jobs (hors registre) | | 200 |
| GET | `/healthz` | sonde | | 200 ou 503 |

Un job dont l'action panique passe `failed` avec `internal error` ; la valeur de la panique n'est écrite que masquée, dans le journal du serveur, qui reste en service. Un paramètre de requête inconnu est un 400 `usage`. Un chemin inconnu sous `/api/` est un 404 `not_found` ; une méthode inadaptée sur un chemin connu, un 405 avec `Allow`. Le `accept` du web répond `"action": "accepted"` là où la CLI répond le nom de la commande. `dedupe apply` du web applique une proposition ; le web n'a pas de `--all-accepted` : l'interface enchaîne les jobs depuis le navigateur, un par proposition acceptée.

### Jobs

Les actions longues (étapes, dedupe, repair) répondent 202 avec un job `{id, action, state, started_at, finished_at, result, error}`, `state` valant `running`, `done` ou `failed`. Un seul job tourne à la fois : un second reçoit 409 `stage_running` (`another long action is running`) ; le verrou d'étape en base protège en plus contre un autre processus. Les 50 derniers jobs sont gardés en mémoire et perdus à l'arrêt ; le résultat durable est en base. Un job tourne dans le contexte du serveur : un arrêt l'annule.

### Erreurs

Corps `{"error": {"code", "message"}}`. Le message est celui de l'erreur typée reconnue ; une erreur inconnue donne `internal error` et le détail masqué n'est écrit que dans le journal du serveur.

| Code | HTTP | CLI | Cas |
|---|---|---|---|
| `usage` | 400 (415 type de contenu, 413 corps) | 2 | requête ou valeur invalide |
| `confirmation_required` | 400 | 2 | `force` sans `confirm` |
| `unauthorized` | 401 | | jeton absent ou faux |
| `forbidden` | 403 | | `Host`, `Origin`, `Sec-Fetch-Site`, `X-Requested-With` |
| `not_found` | 404 | 1 | proposition, groupe, job, route |
| `method_not_allowed` | 405 | | |
| `stale_proposal` | 409 | 3 | proposition d'une génération périmée, candidat devenu `stale` |
| `not_configured` | 409 | 1 | composant absent (fournisseur, client, `dedupe.torrentRoots`) |
| `stage_running` | 409 | 1 | une autre étape ou un autre job tourne |
| `inspection_busy` | 409 | 1 | un examen des liens est encore en cours |
| `refused` | 422 | 4 | règle métier : litige à choisir, pack partiel, proposition non acceptée, non applicable, mauvaise nature, groupe incohérent, client en lecture seule, hors racines… |
| `failure` | 500 | 1 | erreur non typée |
| `shutting_down` | 503 | | le serveur s'arrête |

## Interface

Application Vue embarquée dans le binaire, un seul thème sombre anthracite, routage par fragment (`#/…`) : le serveur ne voit jamais l'onglet. Tout chemin sans extension sert `index.html`. Sans interface construite (`make ui` non lancé), une page « Interface non construite » (« Interface not built » en anglais, selon `language`, avec `<html lang>` assorti) est servie et l'API reste disponible.

| Onglet | Fragment | Équivalent CLI |
|---|---|---|
| Démarrage | `#/demarrage` | `doctor`, `status` |
| Propositions (défaut) | `#/propositions` | `proposals list/accept/reject/choose/accept-group/force` |
| Déduplication | `#/deduplication` | `dedupe plan/apply` |
| Liens | `#/liens` | `links list`, `repair scan/apply` |
| Étapes | `#/etapes` | `scan match verify link seed`, suivi des jobs |
| Sources | `#/sources` | `sources list/test` |

- Mise en page : barre latérale à gauche (icônes seules de 768 à 1023 px, tiroir en dessous), barre d'outils par page, tableaux denses et triables, pastilles d'état (icône et texte, jamais la couleur seule). Sous 768 px chaque ligne devient une carte empilée, sans défilement horizontal.
- Liens : deux onglets. « Liens enregistrés » est paginé par le serveur (200 par page, `links list` avec `--limit` et `--offset`), filtrable par état, sans tri (ordre du serveur : `links list` n'a pas de paramètre de tri, et trier la seule page reçue donnerait un ordre trompeur, donc pas de tri par état par défaut) ; « Non comparé » marque un lien dont rien n'a été comparé, et un examen interrompu (`truncated`) est annoncé. « Réparations » liste les propositions `repair` ; l'application réelle d'une ligne exige de saisir `APPLIQUER` (`APPLY` en anglais), la simulation part directement ; les lignes ambiguës, perdues ou en attente d'un recontrôle du client sont listées mais non applicables.
- Sources : une ligne par source dans l'ordre de la chaîne (ordre, source, type, tracker, état, requêtes et téléchargements sur 24 h face au plafond, cache des capacités, dernière erreur traduite), sans tri. « Tester une source » teste l'indexeur choisi dans la barre d'outils, le bouton de ligne celui de la ligne ; les tests sont désactivés pendant qu'un test tourne, dont le résultat traduit s'affiche ensuite. Le fournisseur n'a pas de bouton de test. Propositions : la colonne Source et le filtre Source (appliqué par le serveur) sont toujours affichés.
- Une boîte demande le jeton sur un 401 ; il vit en mémoire de l'onglet, jamais dans le stockage du navigateur. Un jeton refusé l'efface.
- L'interface redirige vers « Démarrage » au premier lancement (ni candidat ni proposition) ou tant que le diagnostic a un `fail`, sauf choix « Passer ».
- Force n'est actif que pour un `partial_annex` à l'étape `seeded_paused_incomplete`, avec confirmation ; accepter un `partial_pack` passe par une confirmation. `dedupe` et `repair` ont une case « simulation » (dry-run), cochée par défaut, et une confirmation avant d'appliquer pour de bon toutes les propositions acceptées (hors simulation). La page Déduplication va plus loin : « Appliquer les acceptées » ouvre toujours une fenêtre qui liste les propositions (le mode, simulation ou réel, est figé à l'ouverture) ; un remplacement réel, de masse ou d'une seule ligne, n'est envoyé qu'après la saisie du mot `APPLIQUER` (`APPLY` en anglais) ; une simulation d'une ligne part sans confirmation. Les octets à lire sont affichés avant toute acceptation, les lignes non applicables (ambiguë, copie isolée…) restent listées avec un bouton désactivé et leur motif.
- Langue des textes rendus : `language` (`fr` par défaut, ou `en`) fixe la langue des textes destinés à l'opérateur. Les codes d'erreur, les motifs (`reason`), les états, les noms de champs JSON, de commandes et d'options restent en anglais et ne changent jamais avec la langue ; seuls les textes changent : le `message` des erreurs (CLI `--json` et API), la sortie texte de la CLI et son aide d'usage, la `description` du registre des actions. `GET /api/status` porte `language`, que l'interface lit avant tout rendu. Une erreur non typée a un message fixe : « erreur interne (détail dans le journal du serveur) » côté API, dont le détail va au journal ; la CLI, qui n'a pas de journal, écrit « erreur interne » suivi du détail (en anglais) entre parenthèses. Les préfixes `error:` et `warning:` de la CLI restent en anglais : ce sont des marqueurs lisibles par un script. Le rapport de `doctor` (CLI `--json` et `POST /api/doctor/…`) porte `id`, `status`, `code` (stables) et `title`, `detail`, `hint` (traduits). Les avertissements de proposition (`GET /api/proposals`, `proposals list`, résultats de `force`, de `dedupe plan` et de `repair scan`) sont des objets `{"code", "text"}` : `code` est la clé stable du message (`warning.sources_changed`, `warning.application_failed.cross_device`…, `legacy.text` pour une ligne enregistrée avant le catalogue, rendue telle quelle), `text` son rendu dans la langue. Les motifs gardent leur code dans `reason` et `reasons` ; le libellé traduit est ajouté à côté : `reason_labels` (`{code: label}`) à côté de `reasons` dans le résultat d'une étape, `label` à côté de `reason` dans `held` de `status`, dans `excluded_files` d'une proposition et dans les détails dedupe et repair ; un code sans libellé est rendu tel quel. La sortie texte de la CLI affiche le libellé entre parenthèses après le code. Les codes de motif de `links list` sont stables (`link_missing`, `link_target_missing`, `link_inode_diverged`, `link_target_renamed`, `link_not_compared`, `link_replaced`, `link_resized`, `link_target_unreadable`, `link_inspection_failed`) ; `label` porte leur libellé traduit, et la CLI affiche `code (label)`. Les motifs d'une proposition dedupe sans remplacement (`details.reason`) sont `isolated_copy`, `hash_unavailable`, `no_link_kind`, `cross_device`, `link_kind_unsupported`, `link_probe_failed` et `no_atomic_replace`, avec `details.label` ; `details.warnings` liste les clés complètes des avertissements du plan (`warning.dedupe.unreliable_inode`…, `warning.no_client_recheck` ; la valeur courte `no_client_recheck` des anciennes lignes est encore lue), dont le texte est dans `warnings` de la proposition ; le détail `maintenance` d'une proposition (`GET /api/proposals`) porte le même `label`. `warnings` de `status`, du résultat d'une étape (mode cross-seed, `warning.crossseed.*`, `warning.multiple_clients`) et du résultat de `dedupe apply` et de `repair apply` sont des objets `{"code", "text"}`. Les conseils des fournisseurs de montage du diagnostic (`advice.mount.*`) sont rendus dans la langue. Les lignes déjà enregistrées avant cette version gardent leur texte d'origine (`legacy.text` ; un `details.reason` ancien est une phrase anglaise rendue telle quelle).
- Encore non traduits, chacun pour une raison précise : le détail entre parenthèses d'une erreur non typée dans la CLI, et le paramètre `error` des lignes du journal et de `warning.application_failed.detail` (texte `err.Error()` des couches basses : chemins, réponses de services, sans clé stable à traduire ; il est masqué avant affichage) ; le motif de sonde d'un lien dans les signes `✓`/`✗` du diagnostic `link-feasibility` (texte technique anglais du noyau ou du système de fichiers) ; les paramètres `error` du diagnostic (`doctor`) pour la même raison ; les avertissements de plan dedupe enregistrés par une version antérieure (`legacy.text`), qui ne peuvent pas être retrouvés sous une clé ; le détail du texte des erreurs d'usage après leur préfixe typé.
- Journal du serveur : lignes `slog` au format texte sur stderr (`time=… level=… msg="…" code=…`). `msg` est rendu dans la langue configurée, `code` est stable et jamais traduit : `job_failed`, `job_panicked`, `server_listening`, `non_loopback_warning`, `healthcheck_failed`, `api_internal_error`, `api_unauthorized`, `api_panicked` (panique d'un handler synchrone : réponse d'erreur interne générique, valeur et pile jamais renvoyées) et `http_server_error` (lignes de `http.Server.ErrorLog`). Chaque valeur de paramètre (détail d'erreur, adresse) est masquée avant rendu. `server_listening` part sur stdout (`msg="listening on http://<address>"`), les autres sur stderr ; `healthcheck` écrit le mot stable `ok` sur stdout (le même dans les deux langues : un script le lit comme `error:` et `warning:`), ou la ligne `healthcheck_failed` sur stderr (code de sortie 1). Un échec au démarrage de `serve` écrit toujours la ligne CLI simple `error: …`, sans `code`.
- En sortie texte, la CLI retire de tout ce qu'elle affiche les caractères de contrôle (ESC, séquences OSC et CSI, C1, NUL…), saut de ligne et tabulation exceptés : un nom de torrent ou une réponse de service ne peut pas piloter le terminal. La sortie `--json` n'est pas concernée (JSON échappe ces caractères).
