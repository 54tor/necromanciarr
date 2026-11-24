# Sécurité du seed

Seeder un contenu faux pénalise le ratio auprès des trackers, et un client qui écrit dans un fichier lié modifie la bibliothèque. Ces invariants sont vérifiés par le code de Link et Seed, pas par convention. Chaque motif de refus ci-dessous est la raison comptée dans le résultat de l'étape (`reasons`) et ajoutée à la proposition comme avertissement `application failed: <raison>` ; pour `cross_device`, `no_link_dir_on_same_device` et `link_kind_refused`, l'avertissement ajoute entre parenthèses la cause et le réglage à changer (aucun repli vers une autre sorte de lien n'est tenté).

## Les invariants

| # | Invariant | Où |
|---|---|---|
| 1 | Un torrent est **toujours ajouté en pause** (`Start: false`). | Seed |
| 2 | Un torrent n'est **repris qu'à 100 %**, et seulement si la vérification de Necromanciarr a couvert toutes les pièces (nature `full` ou `cross_seed`). | Seed, `RecheckAndGuard` |
| 3 | `skip_checking` n'est demandé que si la vérification est complète, `skipRecheck` vaut `true` et **chaque lien est prouvé par identité de fichier** ; une preuve par empreinte seule (reflink, copie) impose un recontrôle du client. | Seed |
| 4 | Sources, liens et annexes sont **revérifiés juste avant l'ajout**. | Seed |
| 5 | Un `partial_annex` reste **en pause** jusqu'à un Force explicite. Un `partial_pack` n'est **jamais forçable**. | Seed, Force |
| 6 | Un torrent **déjà présent dans le client** n'est repris en main que s'il pointe sur notre dossier ; sinon il est refusé et intact. | Seed |
| 7 | **Aucune écriture dans la bibliothèque.** | partout |
| 8 | Aucune application sans proposition acceptée de la génération courante. | Service |

### 1 et 2 : ajout en pause, reprise à 100 %

Un torrent ajouté ne télécharge rien tant qu'il est en pause. Il n'est repris que dans deux cas :

- `skip_checking` accepté (invariant 3) : le client affiche 100 % par construction, ce qui ne prouve que l'ajout. Seed ne reprend que si l'avancement vaut 1 ; la preuve du contenu est la revérification de l'invariant 4.
- Sinon, `RecheckAndGuard` : pause, recontrôle par le client, puis reprise **uniquement** si l'état final est complet à 100 %. Torrent incomplet, délai dépassé (1 heure), contexte annulé ou erreur : le torrent est remis en pause (même si le client l'avait relancé), la proposition passe `applied` à l'étape `seeded_paused_incomplete` avec l'avertissement `left paused: the client did not reach 100% after its recheck`. Un échec de pause ou de recontrôle est un refus (`pause_failed`, `recheck_failed`, `resume_failed`).

Après une reprise réussie, l'étape est `seeded`. En mode `delegate`, cross-seed est alors prévenu.

### 3 : `skipRecheck`

`skipRecheck` (défaut `true`, importable de cross-seed) ne l'emporte jamais sur le principe de vérification propre : il ne s'applique qu'à un torrent dont toutes les pièces sont vérifiées par nous. Pourquoi revérifier avant l'ajout : avec `skip_checking`, libtorrent vérifie paresseusement ; une source modifiée entre-temps ferait télécharger dans l'inode lié.

### 4 : revérification avant l'ajout

Juste avant l'ajout, Seed :

1. recharge l'association prouvée et compare taille et date (à la nanoseconde) de chaque fichier source ; une source changée rend le candidat `stale`, rien n'est ajouté, avertissement `not seeded: a library file changed since it was linked, verify again` ;
2. contrôle chaque lien du dossier du torrent (même fichier que sa source, ou même taille et même empreinte début-fin pour un reflink) et chaque annexe (taille et SHA-256) ; un dommage supprime l'étape et fait échouer la proposition (`link_changed`, `annex_changed`) : réaccepter puis relancer Link refait le dossier ;
3. refuse un chemin client invalide (`client_path_invalid`, par exemple un caractère interdit par Windows dans un chemin de lecteur).

### 5 : partial_annex et Force

Un `partial_annex` est ajouté en pause, jamais contrôlé par `skip_checking`, et passe `applied` avec l'étape `seeded_paused_incomplete` (`paused_incomplete`). Juste après l'ajout, Seed redemande la pause puis relit le torrent par son hash (jusqu'à 10 essais espacés de 200 ms, l'ajout étant asynchrone) : s'il n'apparaît pas, ou dans un autre état qu'arrêté ou en contrôle, Seed redemande la pause et relit ; toujours en marche, le torrent est retiré du client **sans supprimer ses fichiers** (`deleteFiles=false`, ce sont des liens) et la proposition échoue avec `not_paused_after_add`. Introuvable, elle échoue avec la même raison. Les pièces qui manquent couvrent des annexes absentes de la bibliothèque, souvent à cheval sur la fin d'un média lié. Le client télécharge et écrit chaque pièce manquante **en entier**, y compris la part qui appartient au média : à travers un lien dur ou symbolique, il écrirait des blocs non vérifiés dans le fichier de la bibliothèque.

`Force` est le seul moyen de démarrer ce torrent :

| | CLI | Web |
|---|---|---|
| Action | `proposals force <id> --yes` | `POST /api/proposals/{id}/force` avec `{"confirm": true}` |
| Sans confirmation | avertissement sur stderr, rien n'est démarré, exit 2 (`confirmation_required`) | 400 `confirmation_required` |

Conditions : nature `partial_annex`, décision `applied`, étape `seeded_paused_incomplete`, génération courante, client configuré, sources inchangées (sinon candidat `stale` et refus `stale_proposal`). Un `partial_pack` rend `a partial pack can never be forced`.

Avant de reprendre le torrent, Force isole les médias exposés :

1. il calcule les pièces manquantes : celles qui recouvrent un fichier ni lié ni annexe vérifiée ;
2. chaque média lié qui recouvre une de ces pièces et désigne encore le fichier de la bibliothèque (lien dur, ou lien symbolique) est remplacé par une copie indépendante, écrite dans le dossier de liens du torrent : reflink si le système de fichiers le permet (copie à l'écriture, aucun bloc partagé n'est modifiable), sinon copie complète. Un reflink ou une copie déjà en place n'est pas touché, un média qui ne partage aucune pièce manquante non plus ;
3. une copie complète exige sur le volume du dossier de liens sa taille plus une marge (64 Mio plus 1 % de la taille). Toutes les copies sont préparées dans des fichiers temporaires avant le premier remplacement, puis mises en place par renommage dans le même dossier ; le lien est réenregistré avec sa nouvelle sorte (`reflink` ou `copy`) ;
4. le dossier est revérifié (liens, copies, annexes), puis le client est repris et l'étape devient `forced`. La réponse liste les fichiers remplacés (`replaced`).

Si un média ne peut être ni cloné ni copié (copie refusée, place insuffisante, place libre illisible sur la plateforme), Force refuse avec `force refused, a straddling media file cannot be copied out of the library` (code `refused`) : aucune copie ne reste, aucun lien n'est remplacé, le torrent reste en pause. Un dossier abîmé entre-temps rend `the torrent link folder no longer matches its proven content`. La bibliothèque n'est jamais écrite, ni par l'outil ni par le client. Force tient le verrou d'étape : un second Force, ou toute étape, dedupe ou repair, attend sa fin et échoue d'ici là avec `stage_running`, de sorte que deux copies ne comptent jamais sur la même place libre.

Accepter un `partial_pack` demande déjà un acte explicite (`--allow-partial-pack`, ou `{"allow_partial_pack": true}`) ; il est ajouté en pause comme un `partial_annex` et ne peut jamais être démarré par Necromanciarr.

### 6 : torrents étrangers

Avant d'ajouter, Seed liste les torrents du client. Si l'infohash existe :

- son chemin de sauvegarde égale le nôtre (comparaison nettoyée, casse ignorée pour un chemin Windows) : le torrent est repris en main. Incomplet, il est mis en pause ; complet, il passe par le recontrôle gardé. C'est la reprise après une coupure entre l'ajout et l'enregistrement.
- sinon : refus `already_in_client`, aucune action sur ce torrent.

Un torrent qui n'est pas le nôtre n'est jamais pausé, supprimé ni déplacé. Un même candidat n'est lié qu'une fois (`candidate_already_linked`, `already_linked`).

### 7 : la bibliothèque n'est jamais écrite

- Les fichiers de la bibliothèque sont ouverts en lecture seule (`O_RDONLY`).
- Les liens, annexes et sondes ne sont créés que sous `linkDirs` ou `dedupe.torrentRoots`, qui ne chevauchent jamais une racine de bibliothèque : refus au chargement, à l'étape Link (chemins résolus) et par `doctor`, qui ne lance aucune sonde dans une racine de bibliothèque.
- La base SQLite refuse de vivre dans la bibliothèque (`doctor`, `config-dir`).
- `dedupe` remplace des fichiers **côté torrent** ; `repair apply` ne touche que des liens sous `linkDirs`, `dedupe.torrentRoots` ou le dossier du scan.
- Un hardlink partage l'inode du fichier de la bibliothèque : tout écrivain du client sur ce lien écrit dans le fichier. C'est pourquoi l'invariant 5 existe.

## Passkeys des `.torrent` d'indexeur

Un `.torrent` téléchargé d'un indexeur Torznab porte l'annonce du tracker, donc sa passkey. Il n'est jamais gardé en clair. Match ne télécharge pas encore de `.torrent` d'indexeur : ce qui suit est le format en place pour quand il le fera.

- **Copie assainie** (en clair) : le dictionnaire `info` recopié octet pour octet (jamais décodé puis réencodé : l'infohash ne change pas, même pour un `info` non canonique), plus `creation date`, `encoding` et `piece layers` (des hashes seulement). Tout le reste du premier niveau est retiré : `announce`, `announce-list`, `comment`, `created by`, `url-list`, `httpseeds`, `nodes`, `publisher-url`, `azureus_properties` et toute clé inconnue. Un `.torrent` de plus de 8 Mio, imbriqué au-delà de 64 niveaux, illisible ou dont une clé de premier niveau apparaît deux fois est refusé, sans que l'erreur en cite le contenu.
- **Copie scellée** : le `.torrent` complet chiffré en AES-256-GCM avec `NECROMANCIARR_SECRET_KEY`. Un bloc vaut version (1 octet, valeur 1) ‖ nonce (12 octets) ‖ chiffré ‖ étiquette (16 octets) ; le nonce est tiré de `crypto/rand` à chaque scellement, un aléa défaillant fait échouer le scellement. La donnée associée est l'octet de version suivi de `infohash|tracker|source` (infohash en 40 caractères hexadécimaux minuscules, tracker éventuellement vide, source `provider` ou `torznab:<name>`) ; `|`, les caractères de contrôle et l'UTF-8 invalide sont refusés dans chaque composant, si bien que deux liaisons distinctes ne se confondent jamais ; l'octet de version précède cette donnée associée, donc une version modifiée échoue à l'authentification. Au descellement, la liaison est toujours reconstruite depuis la ligne du candidat (infohash, tracker et source tels qu'enregistrés au scellement), jamais depuis la configuration courante, et le tracker est comparé octet pour octet. Un bloc déplacé vers un autre candidat, tracker ou source, tronqué, altéré ou ouvert avec une autre clé donne une seule erreur, `sealed_torrent_unreadable`, qui ne dit pas laquelle de ces causes.
- **La clé** : 32 octets en base64url sans remplissage (43 caractères), tirés par `necromanciarr api-key --secret` qui l'affiche sur stdout seulement. Seule cette écriture est admise (ni base64 standard, ni hexadécimal, ni espace) ; une valeur invalide est une erreur d'usage qui ne nomme que la variable. La clé n'est jamais sérialisée et elle est masquée partout sous ses écritures probables (brute, base64, hexadécimal). Sa remise à zéro en mémoire n'est qu'un effort : Go ne garantit pas l'absence de copie.
- **Clé perdue ou changée** : les candidats restent lisibles (la copie assainie suffit à Verify), mais Seed refuse un candidat dont le bloc ne se descelle plus et `doctor` (`secret-key`) le signale en `sealed_torrent_unreadable`. Relancer Verify sur ce candidat récupère un `.torrent` neuf dans le budget de l'indexeur. Sauvegarder la clé avec le `.env`.

## Reprise après coupure

| Moment de la coupure | Effet à la relance |
|---|---|
| Pendant Link, dossier à moitié rempli | Les entrées présentes sont comparées à la source : contenu identique adopté et enregistré (un reflink compris) ; entrée enregistrée mais différente retirée ; entrée inconnue : refus `destination_conflict` et rien n'est supprimé. Les entrées périmées ne sont retirées que si le dossier ne contient que des entrées de l'outil. Un échec défait ce que l'exécution avait créé (liens, annexes, dossier). |
| Pendant l'écriture d'une annexe | Écriture sous un nom temporaire `.necromanciarr-tmp-*` puis renommage ; un temporaire laissé est reconnu comme entrée de l'outil et retiré avec les entrées périmées du dossier |
| Entre l'ajout au client et `applied` | Seed retrouve le torrent à notre chemin et le reprend en main (invariant 6) |
| Pendant `dedupe apply` | Le lien est créé sous `<file>.necromanciarr-tmp-<size>-<random>` puis renommé par-dessus ; `dedupe plan` termine le renommage si le fichier torrent manque et que l'entrée a la bonne taille, sinon retire l'entrée temporaire |
| Pendant Verify | Avancement enregistré (64 pièces ou 5 s) : la relance saute les pièces déjà vérifiées si leurs fichiers sont toujours présents |
| Verrou d'étape non rendu | Libéré au plus 2 minutes après la mort du processus |

Si une source change entre Link et Seed, les liens restent sur le disque (ils ne font pas de mal) mais rien n'est ajouté au client.
