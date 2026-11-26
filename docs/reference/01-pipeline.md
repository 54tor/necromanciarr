# Pipeline

Cinq étapes s'exécutent dans cet ordre, chacune par sa commande (`scan`, `match`, `verify`, `link`, `seed`) ou par `POST /api/stages/{stage}/run`. Chaque étape lit et écrit un état persisté dans SQLite (`--db-path`), se rejoue sans doublon et reprend là où une coupure l'a laissée. Rien ne se lance seul : aucune planification interne.

## Règle d'acceptation

**Rien ne s'applique sans proposition acceptée.** Scan, Match et Verify ne produisent que des constats et des propositions. Link et Seed ne traitent que les propositions dont la décision est `accepted`, de la génération courante d'un candidat `verified`. Il n'existe aucun mode automatique.

| Étape | Lit | Écrit | Composant requis |
|---|---|---|---|
| Scan | `libraryRoots`, Radarr, Sonarr | inventaire (chemin, taille, date, TMDB/TVDB) | `libraryRoots` ; `radarr`/`sonarr` pour les identifiants |
| Match | inventaire, fournisseur, indexeurs | candidats `matched` (fournisseur) ou `fetched` (indexeurs) et leurs fichiers | `libraryApi` + `libraryToken` ; `torznab` pour les indexeurs |
| Verify | candidats, `.torrent`, `.nfo`, fichiers locaux | vérifications, associations prouvées, annexes, propositions | `libraryApi` |
| Link | propositions acceptées avec une annonce | dossier du torrent sous `linkDirs`, liens, annexes, étape `linked` | `linkDirs` |
| Seed | propositions `linked` | torrent ajouté au client avec son annonce injectée en mémoire, décision `applied`, étape finale | `torrentClients` ; `trackers` ou `.torrent` scellé |

Un composant absent donne `not_configured` (Match, Verify, Seed) ou l'échec `pipeline: no link directory configured` (Link).

## Scan

1. Parcourt chaque racine. Un lien symbolique en racine est suivi, mais aucun lien symbolique à l'intérieur ne l'est, et un sous-dossier monté sur un autre système de fichiers est ignoré. Seuls les fichiers média sont inventoriés.
2. Un fichier dont la taille ou la date a changé rend `stale` ses candidats (`changed_file_invalidated`). Un fichier absent du disque est retiré et rend ses candidats `stale` (`missing_file_removed`). Si tous les fichiers d'une racine semblent absents (montage décroché), la racine n'est pas élaguée (`empty_root_not_pruned`).
3. Chaque instance Radarr et Sonarr liste ses fichiers avec leurs identifiants TMDB/TVDB. Leurs chemins sont traduits par `pathMappings` (côté `to` vers côté `from`) puis rapprochés des fichiers inventoriés. Deux instances en désaccord : la première valeur vue gagne (`conflicting_ids`). Si une instance répond sans qu'aucun chemin ne corresponde alors que des fichiers ont déjà des identifiants, les identifiants sont conservés (`arr_paths_matched_nothing`) : une règle fausse ne doit pas les effacer.

Le scan enregistre aussi, pour chaque fichier identifié, l'identifiant IMDb, le titre, l'année et, pour Sonarr, la saison et le premier épisode du fichier (deux requêtes par série, saison 0 pour les spéciaux). Les titres, nettoyés des caractères de contrôle et de formatage et limités à 256 caractères, ne servent qu'à la recherche de candidats et ne sont jamais journalisés. Un changement de titre côté instance est repris au scan suivant.

Un fichier sans identifiant n'est pas cherché en v1 (`no_external_id`) : les dix premiers noms sont rendus dans `unidentified_samples`. Les identifiants ne viennent que de Radarr et Sonarr.

## Match

1. Rend `stale` tout candidat dont un fichier local a changé ou disparu.
2. Regroupe les fichiers par identifiant (TMDB, à défaut TVDB) et interroge le fournisseur une fois par identifiant avec les tailles de ces fichiers (filtre `size`, 50 valeurs par requête).
3. Cumule chaque release trouvée sur tous les groupes où elle apparaît, puis ne la garde que si un de ses fichiers a exactement la taille d'un fichier local (`no_exact_size` sinon). Une release est enregistrée une fois, identifiée par (infohash, tracker) : le même infohash chez deux trackers donne deux candidats.
4. Candidat nouveau : `matched`. Candidat connu et associations identiques : inchangé. Associations changées, ou candidat `stale` : associations remplacées, vérification effacée, propositions ouvertes closes, retour à `matched` avec une génération de plus.
5. **Recherche chez les indexeurs** (seulement si `torznab` est configuré) : les fichiers sont regroupés par film (TMDB) ou par saison de série (TVDB + saison). Un groupe n'est cherché que si au moins un de ses fichiers n'a aucun candidat de taille exacte après le fournisseur. Les indexeurs sont interrogés un à un, dans l'ordre de `sourceOrder`, tant qu'un fichier du groupe reste sans `.torrent`. Chez chacun, après la lecture de ses capacités (`t=caps`, une fois par exécution ; décomptée comme une recherche seulement quand la requête part vraiment, pas quand le client la sert de son cache de 24 h) : la recherche par identifiant si les capacités l'annoncent (`t=movie&tmdbid=` ou `imdbid=` ; `t=tvsearch&tvdbid=`, avec `season` et `ep` seulement s'ils sont connus), puis la recherche texte (`q=<title> <year>` pour un film, `q=<title> S<ss>E<ee>` pour un épisode seul, `S<ss>` pour plusieurs fichiers d'une saison, le titre seul si la saison est inconnue ou pour un spécial : jamais `S00E00`), arrêt au premier `.torrent` retenu. Seules partent les catégories annoncées par les capacités : celles de `categories` qui y figurent, sinon les catégories films (2000 à 2999) ou séries (5000 à 5999) annoncées ; aucune ne reste : l'indexeur n'est pas interrogé pour ce groupe.
   - Filtre avant tout téléchargement, sur la taille annoncée : au moins la taille du plus gros fichier du groupe ; pour un film seul, au plus cette taille + 1 % + 64 Mio (annexes) ; aucun plafond pour une série (pack). Les résultats gardés sont téléchargés un à un, du plus proche au plus éloigné en taille ; chaque téléchargement attend `minInterval` comme une recherche, et un élément (même GUID) n'est pas téléchargé deux fois pour un groupe. Au plus 3 téléchargements par groupe et par exécution, tous indexeurs confondus : atteint, le groupe est clos pour l'exécution, et un groupe de leurres ne prive pas les suivants du budget.
   - Un `.torrent` n'est retenu que si un de ses fichiers a **exactement** la taille d'un fichier local du groupe encore sans candidat (un `.torrent` qui ne prouve qu'un fichier déjà couvert, par exemple le pack du fournisseur servi par un indexeur sans `tracker`, est écarté et la recherche continue) : une taille totale juste ne suffit pas (`torznab_no_exact_size`, `.torrent` écarté, jamais enregistré). v2 seul, invalide, hôte de téléchargement étranger, plus de 8 Mio : écarté avec sa raison (`v2_only`, `invalid_torrent`, `foreign_download_host`, `torrent_too_large`) ; magnet seul : compté (`magnet_only`). Le nom du candidat est le titre de l'indexeur ; le titre d'un indexeur contenant un passkey de l'annonce (8 octets ou plus, brut, encodé ou en autre casse) est remplacé par le nom du torrent ; un torrent dont le nom ou un chemin contient un passkey est refusé (`invalid_torrent`).
   - Le candidat retenu est enregistré directement à `fetched`, en une seule écriture : associations, `source` = `torznab:<name>`, `tracker` = celui déclaré pour l'indexeur, sinon `torznab:<name>`, copie sans annonce dans `torrent`, copie complète scellée dans `torrent_sealed` (aucune si une entrée `trackers` couvre ce tracker). Un candidat de même (infohash, tracker) déjà connu, du fournisseur ou d'un indexeur précédent, reste tel quel : le résultat est écarté ; seul un candidat `stale` est repris, comme par le fournisseur (propositions ouvertes closes, vérification effacée, associations remplacées, génération suivante), puis repasse à `fetched` avec la copie téléchargée.
   - Sans `NECROMANCIARR_SECRET_KEY`, aucun indexeur n'est sollicité, même pour un tracker couvert par `trackers` (`secret_key_missing`, compté une fois) : défense en profondeur, le chargement de la configuration exige déjà la clé dès que `torznab` est configuré. Budget épuisé (`indexer_budget_exhausted`, compté une fois par indexeur et par exécution) : pas un échec, l'indexeur n'est plus sollicité et les groupes restants attendent la prochaine exécution. Indexeur indisponible ou délai dépassé, accès refusé, réponse hostile ou illisible à une recherche : écarté pour le reste de l'exécution, comme pour le secours de Verify. Un groupe ou un indexeur en échec n'arrête jamais l'étape.
   - Le résultat de l'étape donne `indexers` : par source, `torznab_queries`, `torznab_downloads`, `budget_exhausted`, `magnet_only`, `cache_hits` (des comptes, jamais d'URL ni de clé).
   - **Cache et reprise** : chaque réponse de recherche réussie est gardée en base (`indexer_cache`) sous la clé SHA-256 de la requête normalisée (indexeur, mode, texte en minuscules aux espaces réduits, identifiants, catégories triées ; jamais la clé d'API) : 6 h, 24 h si la réponse est vide ; une erreur n'est jamais gardée. Le cache ne garde, par résultat, que l'identifiant du flux (empreinte), l'infohash annoncé et la taille : ni lien de téléchargement, ni clé, ni magnet, ni titre ; au plus 150 résultats et 64 Kio par réponse, le reste est ignoré, 2000 lignes au plus par indexeur. Une réponse en cache évite la requête (ni quota ni attente `minInterval`, comptée dans `cache_hits`) ; elle ne contient aucun lien, donc si un de ses résultats passe le filtre de taille et que son infohash n'est pas encore un candidat, la recherche est renvoyée pour obtenir un lien frais, et sa réponse remplace la ligne. Un `.torrent` dont l'infohash annoncé est déjà un candidat du même tracker (sauf `stale`, repris) n'est jamais retéléchargé ; un résultat sans infohash annoncé ne peut être reconnu qu'après téléchargement. Quand la recherche d'un groupe chez un indexeur va au bout (réponse reçue, même vide, ou arrêt sur le plafond de 3 téléchargements du groupe ; pas quand le budget, une panne ou un refus l'interrompt), le groupe est marqué (`indexer_searched`) pour 7 jours : la prochaine exécution reprend aux groupes non cherchés. La marque dépend des identifiants, de la saison et des tailles encore sans candidat, donc une bibliothèque modifiée est cherchée de nouveau ; un candidat d'indexeur qui devient `stale` efface les marques.
   - Ordre de grandeur avec les défauts (`maxQueriesPerRun` 30, `maxDownloadsPerRun` 10, `minInterval` 3 s) : au plus deux recherches par groupe non couvert et par indexeur, soit au plus 40 requêtes par indexeur et par exécution (30 recherches, capacités comprises, et 10 téléchargements), environ 2 minutes par indexeur au rythme de `minInterval` ; les quotas par jour (200 et 50) bornent les relances.
6. Recalcule les groupes.

Fournisseur limité (429) ou indisponible : l'étape s'arrête proprement avec `RetryAfter`, tout ce qui est trouvé est déjà enregistré, la relance reprend. Accès refusé ou contexte annulé : arrêt. Une recherche en échec pour une autre raison est comptée (`search_failed`) et l'étape continue ; après une exécution incomplète, un candidat connu n'est pas réassocié si l'association cumulée perdrait des fichiers (`update_deferred_incomplete_run`).

## Verify

Traite un par un chaque candidat `matched` ou `fetched`.

1. **Fichiers inchangés** : taille et date de chaque fichier associé sont comparées à l'association ; sinon `stale` (`changed_file_stale`).
2. **`.torrent`** : repris de la base s'il est déjà `fetched`, sinon téléchargé (8 Mio au plus conservés) et le candidat passe à `fetched`. Un torrent v2 seul, invalide, ou dont l'infohash diffère du candidat rend le candidat `failed` (`v2_only`, `invalid_torrent`, `infohash_mismatch`), de même `torrent_not_found`, `torrent_fetch_failed` et `torrent_too_large`.
   - **Secours par les indexeurs** : sur le seul `torrent_not_found` du fournisseur (aucune autre erreur), Verify interroge, dans l'ordre de `sourceOrder`, les indexeurs dont `tracker` égale celui du candidat (casse et espaces ignorés) ; un indexeur sans `tracker`, ou un candidat sans tracker, n'est jamais concerné. Une recherche texte par indexeur (`q=` nom de la release, caractères de contrôle retirés, 256 caractères au plus), puis au plus 3 `.torrent` téléchargés par candidat, tous indexeurs confondus ; un élément dont l'infohash annoncé diffère n'est pas téléchargé. Seul un `.torrent` dont l'infohash **calculé** égale celui du candidat est retenu, le premier trouvé : la base garde sa copie sans annonce dans `torrent`, la copie complète scellée dans `torrent_sealed` et `source` passe à `torznab:<name>`. Les éléments qui annoncent l'infohash du candidat sont essayés d'abord, puis ceux sans attribut dans l'ordre du flux ; une fois les 3 téléchargements dépensés, les indexeurs suivants ne sont pas interrogés. Si une entrée `trackers` couvre le tracker du candidat, rien n'est scellé (l'annonce viendra de la configuration) et `NECROMANCIARR_SECRET_KEY` n'est pas exigée pour ce secours ; sinon, sans elle, aucun indexeur n'est interrogé (`secret_key_missing`, candidat `failed`). Le résultat de l'étape donne `searched_sources` (identifiants des sources interrogées, par candidat) ; les éléments refusés sont comptés (`magnet_only`, `v2_only`, `invalid_torrent`, `infohash_mismatch`, `foreign_download_host`, `torrent_too_large`) seulement pour un candidat resté introuvable, alors `failed` (`torrent_not_found`). Un indexeur laissé en route laisse le candidat `matched`, repris à la prochaine exécution. Budget épuisé (`indexer_budget_exhausted`) : plus aucune requête à cet indexeur dans l'exécution. Panne sans `Retry-After` ou délai dépassé (`indexer_unavailable`), accès refusé (`indexer_access_denied`), réponse hostile à une recherche (`indexer_hostile_response`) ou illisible (`torrent_fetch_failed` : page HTML, mauvais type, corps trop gros) : l'indexeur est écarté pour le reste de l'exécution, et chaque candidat suivant de son tracker porte la même raison sans nouvelle requête. Un `Retry-After` l'écarte jusqu'à son terme (une heure au plus), au-delà de l'exécution. Un `.torrent` refusé au téléchargement (v2 seul, invalide) n'écarte que l'élément.
3. **Association par hash** : fichier local et fichier du torrent sont d'abord appariés par taille. Si plusieurs fichiers locaux ont la même taille, les pièces sont hachées pour départager, dans la limite de `tieBreakReadBytes` octets par candidat (50 Gio par défaut) : au-delà, la raison `read_budget_exceeded` est comptée, les fichiers non départagés restent ambigus (litige `conflict`) et les fichiers déjà prouvés poursuivent la vérification. Sans fichier apparié, le candidat est `failed` (`no_file_matched`). Un fichier encore ambigu reste ambigu : il n'est jamais choisi.
4. **`.nfo` manquants** : les `.nfo` du torrent absents de la bibliothèque sont demandés au fournisseur. Chacun est essayé sous quatre variantes d'octets (brut, sans BOM, fins de ligne LF, CRLF), de même taille que le fichier du torrent. Une variante n'est retenue que si toutes les pièces qui recouvrent le fichier concordent, ce qui exige que tous les autres fichiers de ces pièces soient disponibles. Elle est alors conservée en base (1 Mio au plus) comme annexe vérifiée. Sinon le `.nfo` reste manquant.
5. **Couverture** : chaque pièce dont tous les fichiers sont disponibles est hachée en SHA1 et comparée au `.torrent`. Les octets d'une pièce à cheval sur un fichier absent ne sont jamais comptés comme possédés. La lecture passe par le limiteur `readBytesPerSecond` ; l'avancement est enregistré toutes les 64 pièces ou 5 secondes.
6. **Verdict** : voir le tableau des natures. Un média présent dont une pièce a un SHA1 faux rend le candidat `failed` (`media_hash_failed`) sans proposition ; une annexe présente dans ce cas est exclue de l'association et du lien (`annex_hash_failed`) et le client la téléchargera. Les fichiers sont revérifiés à la fin : s'ils ont changé pendant la lecture, le candidat est `stale`.

Une lecture locale en échec (`read_error`) ne change pas l'état du candidat : sa vérification enregistrée sert de reprise à la relance.

## Natures de proposition

| Nature | Créée par | Sens | Décision possible |
|---|---|---|---|
| `full` | Verify | Toutes les pièces vérifiées, aucun média manquant | `accept`, `reject`, `choose` |
| `partial_annex` | Verify | Tous les médias présents et vérifiés ; des pièces d'annexes manquent | `accept`, `reject`, puis `force` après seed |
| `partial_pack` | Verify | Des médias du torrent ne sont pas dans la bibliothèque | `accept --allow-partial-pack` seulement ; jamais forçable |
| `conflict` | Verify, groupes | Plusieurs torrents possibles pour un même fichier, ou fichier local ambigu | `choose` (jamais `accept`) |
| `cross_seed` | groupes | Mode `native` : autre torrent complet du groupe | `accept`, ou `accept-group` |
| `dedupe` | `dedupe plan` | Remplacer une copie côté torrent par un lien | `dedupe apply` |
| `repair` | `repair scan` | Refaire un lien cassé vers sa nouvelle cible | `repair apply` |

Verify ne crée aucune proposition quand les médias ne sont pas tous couverts sans que ce soit un pack partiel (`media_not_verified`) : le candidat reste `verified` sans proposition.

### Plafond de pièces manquantes des partiels

`partialMaxMissingPercent` (défaut `100`, sans plafond) borne la part de pièces manquantes (pièces manquantes / pièces du torrent) d'un `partial_annex` ou `partial_pack`. Égal au plafond reste proposé ; au-delà, le candidat reste `verified`, aucune proposition n'est créée et son verdict est mis de côté avec la raison `missing_pieces_above_cap` : comptée dans le résultat de Verify (`reasons`) et listée par `status` (champ `held` : nature, pièces manquantes, raison). Les torrents `full` et les litiges ne sont jamais plafonnés.

Chaque Verify applique le plafond courant, sans relire les fichiers : un verdict mis de côté revenu sous le plafond devient une proposition `pending` ; une proposition partielle encore `pending` passée au-dessus devient `superseded` et son verdict est mis de côté. Une proposition déjà acceptée, appliquée, rejetée ou en échec n'est jamais retirée. Un verdict mis de côté ne vaut que pour la génération du candidat : une revérification le remplace.

### Décisions

```
pending  -> accepted | rejected | superseded
accepted -> applied  | failed   | superseded
rejected -> superseded
failed   -> accepted (réacceptation)
```

- `accept` sur une proposition `failed` la remet en attente puis l'accepte ; sur une proposition déjà `accepted` ou `applied`, il ne fait rien.
- `reject` n'est possible que depuis `pending`. Une proposition acceptée ne se reprend pas en v1.
- `applied` est posé par Seed (étape `seeded`, `seeded_paused_incomplete`) ou par l'action dedupe/repair. L'étape `linked` laisse la décision à `accepted`.
- `superseded` clôt une proposition dont le candidat est devenu périmé ou réassocié. Elle ne ressuscite jamais.
- Au plus une proposition non close par candidat, nature et génération. Une proposition décidée (`rejected`, `applied`) d'une génération antérieure ne bloque pas la nouvelle.
- Étapes d'application (`step`) : `linked`, `seeded`, `seeded_paused_incomplete`, `forced`.

## Générations et péremption

La génération d'un candidat augmente à chaque invalidation. Une proposition porte la génération de sa création ; `current` vaut vrai seulement si elle égale celle d'un candidat `verified`. Une proposition périmée ne s'accepte pas, ne se lie pas, ne se seed pas, ne se force pas : l'action est refusée avec `stale_proposal` (exit 3, HTTP 409) et il faut revérifier.

Causes de péremption : fichier local modifié, disparu ou remplacé (Scan, Match, Verify), associations différentes (Match), source modifiée détectée par Link, Seed ou Force. Le candidat passe `stale`, ses propositions `pending`, `accepted` et `failed` passent `superseded`, sa vérification et son association prouvée sont effacées. Un candidat `failed` ne redevient `matched` que si ses associations changent.

## Groupes

Les candidats qui se disputent un même fichier local forment un groupe (`group_id`) : épisode seul et pack de saison, même release chez deux trackers. Le groupe est recalculé par Match et Verify.

- **Principale** : la proposition `full` ouverte au plus fort pourcentage vérifié ; à égalité, le plus petit candidat. Seule une `full` peut l'être. `proposals choose <id>` la fixe à la main et l'élection automatique ne la remplace plus tant qu'elle reste ouverte.
- **Litige** : dès que deux membres du groupe ont une proposition, une proposition `conflict` liste tous les torrents du groupe sans en choisir un. Son accepter est refusé (`refused`) : on choisit.
- **Un torrent par fichier** : quand le comportement effectif de cross-seed est `disabled` (mode `off`, ou `delegate` sans démon joignable), une seule proposition de torrent acceptée ou appliquée par groupe (`cross-seed is disabled, another torrent of this group is already accepted`). Dans tous les modes, un même candidat n'a jamais deux propositions de torrent acceptées.
- **`accept-group`** : accepte la principale. En mode `native`, accepte aussi, pour chaque autre torrent vérifié, sa proposition `cross_seed` (à défaut sa `full`).
- Une proposition ouverte rattachée à un autre groupe que son candidat bloque Link et Seed (refus `refused`, `proposal group differs from its candidate group`) tant que Match ou Verify n'a pas resynchronisé.

## Stockage des `.torrent`

| Colonne de `torrent_candidates` | Contenu | Effacement |
|---|---|---|
| `source` | `provider`, ou `torznab:<name>` pour un indexeur : l'origine du `.torrent` conservé, qui peut différer de la source vue au Match (secours de Verify), car le scellement y est lié. Sinon la première source qui a fourni le candidat, gardée ensuite. Les candidats d'une base antérieure valent `provider` | jamais |
| `torrent` | `.torrent` sans annonce : Verify n'y conserve que la copie assainie (`info` recopié à l'identique, donc même infohash), pour le fournisseur comme pour un indexeur ; un `.torrent` que l'assainissement refuse rend le candidat `failed` (`invalid_torrent`) sans arrêter l'étape. Garde-fou de la colonne : un premier niveau qui porte `announce`, `announce-list`, `url-list`, `httpseeds` ou `comment`, ou illisible, est refusé (`ErrUnsanitizedTorrent`) ; un fichier nommé `announce` dans `info` est accepté. 8 Mio au plus | avec le candidat |
| `torrent_sealed` | `.torrent` complet d'un indexeur (il porte la passkey), scellé par `NECROMANCIARR_SECRET_KEY` et lié à `infohash`, `tracker` et `source` de la ligne ; 8 Mio + 128 octets au plus, garantis par la base | voir ci-dessous |

Le bloc scellé est effacé par la base, dans l'instruction même qui écrit l'un de ces changements, sans fenêtre où l'un irait sans l'autre :

- le candidat passe `failed` ou `stale`, avec ou sans proposition (une relance retélécharge le `.torrent`) ;
- une proposition du candidat passe `applied`, `rejected` ou `superseded` et il ne lui reste aucune autre proposition `pending`, `accepted` ou `failed` hors `conflict` (un litige ne se seed jamais) : un litige rejeté ne l'efface pas tant que la `full` du même candidat reste ouverte ;
- un tracker qu'une entrée `trackers` couvre désormais (tracker comparé octet pour octet à la valeur stockée) : au démarrage du service et à chaque lecture des propositions.

Une proposition dont le candidat n'a ni entrée `trackers` ni bloc scellé est `no_announce` : Verify la compte sous ce motif, chaque lecture la marque `seedable: false`, Link la refuse avant de créer quoi que ce soit, comme une proposition au `.torrent` scellé sans `NECROMANCIARR_SECRET_KEY` (`secret_key_missing`) ([03-seed-safety.md](03-seed-safety.md)). Redémarrer le service après avoir modifié `trackers`.

Une proposition `pending` ou `failed` jamais décidée garde le bloc, puisque Seed en a besoin ; pour que rien ne reste indéfiniment, chaque Verify efface d'abord tout bloc dont le candidat n'a pas été mis à jour depuis 30 jours.

## Reprise, verrou, plafonds

- **Verrou d'étape** : une seule étape, ou une action `dedupe plan/apply` ou `repair scan/apply`, tourne à la fois, quel que soit le processus (CLI, serveur). Le bail dure 2 minutes et est prolongé toutes les 40 secondes. Un second lancement échoue avec `stage_running` (CLI exit 1, HTTP 409). Un processus mort libère la place après le bail. Si le bail ne peut plus être prolongé (reprise par un autre, 3 échecs de suite), l'étape s'arrête avec `pipeline: run lock lost` et n'écrit plus.
- **Interruption** : Ctrl-C ou SIGTERM annule l'action en cours et l'état cohérent est enregistré ; un second Ctrl-C tue le processus. Relancer l'étape reprend.
- **Débit de lecture** : `readBytesPerSecond` (20 Mio/s par défaut) borne les lectures de Verify, de la déduplication et des empreintes de lien, rafale 1 Mio. `tieBreakReadBytes` (50 Gio) borne seulement le départage des copies de même taille ; la couverture d'un candidat n'a pas de plafond d'octets.
- **Fournisseur** : 4 requêtes simultanées, 30 s par requête, réponses JSON de 16 Mio et `.torrent` de 32 Mio au plus, cache de 1 h pour les succès et 10 min pour les 404.
- **Recontrôle client** : une heure au plus ; au-delà le torrent reste en pause.
- **Examen des liens** (`links list`) : 20 s ; un seul examen à la fois.
- **Diagnostic** : 10 s par contrôle.
