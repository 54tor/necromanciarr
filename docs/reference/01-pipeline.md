# Pipeline

Cinq étapes s'exécutent dans cet ordre, chacune par sa commande (`scan`, `match`, `verify`, `link`, `seed`) ou par `POST /api/stages/{stage}/run`. Chaque étape lit et écrit un état persisté dans SQLite (`dbPath`), se rejoue sans doublon et reprend là où une coupure l'a laissée. Rien ne se lance seul : aucune planification interne.

## Règle d'acceptation

**Rien ne s'applique sans proposition acceptée.** Scan, Match et Verify ne produisent que des constats et des propositions. Link et Seed ne traitent que les propositions dont la décision est `accepted`, de la génération courante d'un candidat `verified`. Il n'existe aucun mode automatique.

| Étape | Lit | Écrit | Composant requis |
|---|---|---|---|
| Scan | `libraryRoots`, Radarr, Sonarr | inventaire (chemin, taille, date, TMDB/TVDB) | `libraryRoots` ; `radarr`/`sonarr` pour les identifiants |
| Match | inventaire, fournisseur | candidats `matched` et leurs fichiers | `libraryApi` + `LIBRARY_PROVIDER_TOKEN` |
| Verify | candidats, `.torrent`, `.nfo`, fichiers locaux | vérifications, associations prouvées, annexes, propositions | `libraryApi` |
| Link | propositions acceptées | dossier du torrent sous `linkDirs`, liens, annexes, étape `linked` | `linkDirs` |
| Seed | propositions `linked` | torrent ajouté au client, décision `applied`, étape finale | `torrentClients` |

Un composant absent donne `not_configured` (Match, Verify, Seed) ou l'échec `pipeline: no link directory configured` (Link).

## Scan

1. Parcourt chaque racine. Un lien symbolique en racine est suivi, mais aucun lien symbolique à l'intérieur ne l'est, et un sous-dossier monté sur un autre système de fichiers est ignoré. Seuls les fichiers média sont inventoriés.
2. Un fichier dont la taille ou la date a changé rend `stale` ses candidats (`changed_file_invalidated`). Un fichier absent du disque est retiré et rend ses candidats `stale` (`missing_file_removed`). Si tous les fichiers d'une racine semblent absents (montage décroché), la racine n'est pas élaguée (`empty_root_not_pruned`).
3. Chaque instance Radarr et Sonarr liste ses fichiers avec leurs identifiants TMDB/TVDB. Leurs chemins sont traduits par `pathMappings` (côté `to` vers côté `from`) puis rapprochés des fichiers inventoriés. Deux instances en désaccord : la première valeur vue gagne (`conflicting_ids`). Si une instance répond sans qu'aucun chemin ne corresponde alors que des fichiers ont déjà des identifiants, les identifiants sont conservés (`arr_paths_matched_nothing`) : une règle fausse ne doit pas les effacer.

Un fichier sans identifiant n'est pas cherché en v1 (`no_external_id`) : les dix premiers noms sont rendus dans `unidentified_samples`. Les identifiants ne viennent que de Radarr et Sonarr.

## Match

1. Rend `stale` tout candidat dont un fichier local a changé ou disparu.
2. Regroupe les fichiers par identifiant (TMDB, à défaut TVDB) et interroge le fournisseur une fois par identifiant avec les tailles de ces fichiers (filtre `size`, 50 valeurs par requête).
3. Cumule chaque release trouvée sur tous les groupes où elle apparaît, puis ne la garde que si un de ses fichiers a exactement la taille d'un fichier local (`no_exact_size` sinon). Une release est enregistrée une fois, identifiée par (infohash, tracker) : le même infohash chez deux trackers donne deux candidats.
4. Candidat nouveau : `matched`. Candidat connu et associations identiques : inchangé. Associations changées, ou candidat `stale` : associations remplacées, vérification effacée, propositions ouvertes closes, retour à `matched` avec une génération de plus.
5. Recalcule les groupes.

Fournisseur limité (429) ou indisponible : l'étape s'arrête proprement avec `RetryAfter`, tout ce qui est trouvé est déjà enregistré, la relance reprend. Accès refusé ou contexte annulé : arrêt. Une recherche en échec pour une autre raison est comptée (`search_failed`) et l'étape continue ; après une exécution incomplète, un candidat connu n'est pas réassocié si l'association cumulée perdrait des fichiers (`update_deferred_incomplete_run`).

## Verify

Traite un par un chaque candidat `matched` ou `fetched`.

1. **Fichiers inchangés** : taille et date de chaque fichier associé sont comparées à l'association ; sinon `stale` (`changed_file_stale`).
2. **`.torrent`** : repris de la base s'il est déjà `fetched`, sinon téléchargé (8 Mio au plus conservés) et le candidat passe à `fetched`. Un torrent v2 seul, invalide, ou dont l'infohash diffère du candidat rend le candidat `failed` (`v2_only`, `invalid_torrent`, `infohash_mismatch`), de même `torrent_not_found`, `torrent_fetch_failed` et `torrent_too_large`.
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

## Reprise, verrou, plafonds

- **Verrou d'étape** : une seule étape, ou une action `dedupe plan/apply` ou `repair scan/apply`, tourne à la fois, quel que soit le processus (CLI, serveur). Le bail dure 2 minutes et est prolongé toutes les 40 secondes. Un second lancement échoue avec `stage_running` (CLI exit 1, HTTP 409). Un processus mort libère la place après le bail. Si le bail ne peut plus être prolongé (reprise par un autre, 3 échecs de suite), l'étape s'arrête avec `pipeline: run lock lost` et n'écrit plus.
- **Interruption** : Ctrl-C ou SIGTERM annule l'action en cours et l'état cohérent est enregistré ; un second Ctrl-C tue le processus. Relancer l'étape reprend.
- **Débit de lecture** : `readBytesPerSecond` (20 Mio/s par défaut) borne les lectures de Verify, de la déduplication et des empreintes de lien, rafale 1 Mio. `tieBreakReadBytes` (50 Gio) borne seulement le départage des copies de même taille ; la couverture d'un candidat n'a pas de plafond d'octets.
- **Fournisseur** : 4 requêtes simultanées, 30 s par requête, réponses JSON de 16 Mio et `.torrent` de 32 Mio au plus, cache de 1 h pour les succès et 10 min pour les 404.
- **Recontrôle client** : une heure au plus ; au-delà le torrent reste en pause.
- **Examen des liens** (`links list`) : 20 s ; un seul examen à la fois.
- **Diagnostic** : 10 s par contrôle.
