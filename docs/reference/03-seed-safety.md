# Sécurité du seed

Seeder un contenu faux pénalise le ratio auprès des trackers, et un client qui écrit dans un fichier lié modifie la bibliothèque. Ces invariants sont vérifiés par le code de Link et Seed, pas par convention. Chaque motif de refus ci-dessous est la raison comptée dans le résultat de l'étape (`reasons`) et ajoutée à la proposition comme avertissement `application failed: <raison>` ; pour `cross_device`, `no_link_dir_on_same_device` et `link_kind_refused`, l'avertissement ajoute entre parenthèses la cause et le réglage à changer (aucun repli vers une autre sorte de lien n'est tenté).

## Les invariants

| # | Invariant | Où |
|---|---|---|
| 1 | Un torrent est **toujours ajouté en pause** (`Start: false`). | Seed |
| 2 | Un torrent n'est **repris qu'à 100 %**, et seulement si la vérification de Necromanciarr a couvert toutes les pièces (nature `full` ou `cross_seed`). | Seed, `RecheckAndGuard` |
| 3 | `skip_checking` n'est demandé que si la vérification est complète, `skipRecheck` vaut `true` et **chaque lien est prouvé par identité de fichier** ; une preuve par empreinte seule (reflink, copie) impose un recontrôle du client. Un lien distant (`sftp://`) n'est **jamais** prouvé, même quand le volume rend une identité de fichier (`identity_unproven`). | Seed |
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

Un dossier de liens distant (`linkDirs` en `sftp://<provider>/<absolute path>`) ne donne jamais `skip_checking` : SFTP ne rend pas d'inode, et même une identité rendue par le serveur (`RemoteStat`, inode lu par `ssh exec stat`) ne prouve pas que le client lit ce fichier ; de même, une réparation trouvée par un inode distant est proposée sans jamais être appliquée d'office. Le lien est enregistré avec son volume (colonne `volume` de `links`), donc reste non prouvé d'une exécution à l'autre ; avec `skipRecheck` et une vérification complète, Seed compte `identity_unproven`, ajoute le torrent en pause sans `skip_checking`, et `RecheckAndGuard` ne le reprend qu'à 100 %. Force reste soumis aux mêmes règles (`partial_annex` en pause, `partial_pack` jamais) ; sur un volume distant, il refuse l'isolation (voir « Force sur un volume SFTP » ci-dessous).

### 4 : revérification avant l'ajout

Juste avant l'ajout, Seed :

1. recharge l'association prouvée et compare taille et date (à la nanoseconde) de chaque fichier source ; une source changée rend le candidat `stale`, rien n'est ajouté, avertissement `not seeded: a library file changed since it was linked, verify again` ;
2. contrôle chaque lien du dossier du torrent (même fichier que sa source, ou même taille et même empreinte début-fin pour un reflink) et chaque annexe (taille et SHA-256) ; un dommage supprime l'étape et fait échouer la proposition (`link_changed`, `annex_changed`) : réaccepter puis relancer Link refait le dossier ;
3. refuse un chemin client invalide (`client_path_invalid`, par exemple un caractère interdit par Windows dans un chemin de lecteur) ; pour un dossier de liens distant, exige une règle `pathMappings` dont `from` est une adresse `sftp://` du même fournisseur, sinon refuse (`client_path_unmapped`) : « même chemin » n'a pas de sens entre deux machines ;
4. un volume distant qui ne répond pas (`remote_unavailable`) laisse la proposition liée, sans échec ni étape effacée : la relance la reprend. Link fait de même : la proposition reste acceptée. Verify aussi : le candidat n'est jamais marqué `stale` faute de pouvoir relire ses fichiers.

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

Force sur un volume SFTP : quand un média exposé doit être copié dans un dossier de liens distant, Force est refusé avant toute écriture avec `server_copy_unavailable` (« Forcer est impossible sur seedbox : le serveur n'offre pas la copie côté serveur (copy-data) », code `refused`) : le client SFTP n'envoie pas `copy-data`, et une copie par flux ferait passer tout le média par cet hôte, deux fois à travers le réseau, et pourrait laisser un fichier à moitié copié. Aucune copie n'est tentée, rien n'est écrit, la proposition et son étape ne changent pas. Une place libre illisible (serveur sans `statvfs`) donne `free_space_unknown`. Que faire : lancer Necromanciarr sur la seedbox elle-même, avec un dossier de liens local au serveur, ou garder ce torrent en pause en attendant la prise en charge de `copy-data`. Force sans média à copier (aucun lien exposé) n'est pas concerné.

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

Sur un volume SFTP (`sftp://`) :

- La bibliothèque n'est lue que par des ouvertures en lecture seule (`SSH_FXF_READ`). Toute requête d'écriture (création, `Setstat`, renommage, suppression, dossier, lien) vise un dossier de liens, une racine torrent ou le dossier d'un scan de repair.
- Trois gardes l'imposent. Le registre refuse une racine d'écriture qui recouvre une racine de bibliothèque, chemins résolus par le serveur (`RealPath`). Le volume refuse lui-même, avant toute requête, un dossier qui recouvre les racines de bibliothèque de son fournisseur. La sonde (lien symbolique, lien dur, remplacement atomique, `statvfs`) tourne une fois par session, dans la première racine d'écriture ouverte, avant qu'elle soit rendue à une étape ; `dedupe plan` ouvre la racine torrent pour la déclencher quand rien n'est encore sondé. Un échec de la sonde refuse la racine, rien n'est écrit. Hypothèse : le résultat vaut pour tout le volume pendant la session (un même serveur offre les mêmes fonctions dans tous ses dossiers).
- Un lien dur garde sa source dans la bibliothèque : seuls son nombre de liens et son ctime changent, comme en local. Un lien symbolique (lien `symlink`, lien réparé) désigne un fichier de la bibliothèque sans l'écrire.
- Preuve : un test de bout en bout fait passer scan, Match, Verify, Link, Seed, Force, dedupe (essai puis réel), repair et `doctor` par un serveur SFTP en processus. Il exige du journal du serveur zéro requête d'écriture sous la racine de bibliothèque telle que le serveur la résout, et un arbre de bibliothèque identique (contenu, droits, date, inode). Chaque étape doit prouver qu'elle a travaillé, et un écrivain malveillant planté dans le test est vu par les deux contrôles.
- Risque accepté : SFTP n'a pas d'`openat`. Entre le contrôle d'un dossier parent et l'écriture, un acteur qui écrit déjà dans le dossier de liens distant peut remplacer ce parent par un lien vers la bibliothèque. Défense recommandée : un compte SFTP sans droit d'écriture sur la bibliothèque (ACL, groupe, montage en lecture seule côté serveur).

## Passkeys des `.torrent` d'indexeur

Un `.torrent` téléchargé d'un indexeur Torznab porte l'annonce du tracker, donc sa passkey. Il n'est jamais gardé en clair. Match en télécharge pour les fichiers que le fournisseur ne couvre pas, dans le budget de l'indexeur, et ne l'enregistre à `fetched` qu'après la preuve de taille exacte. Selon `trackers`, la copie complète est scellée (aucune si une entrée `trackers` couvre ce tracker) ; ce qui suit est le format de ces copies.

- **Copie assainie** (en clair) : le dictionnaire `info` recopié octet pour octet (jamais décodé puis réencodé : l'infohash ne change pas, même pour un `info` non canonique), plus `creation date`, `encoding` et `piece layers` (des hashes seulement). Tout le reste du premier niveau est retiré : `announce`, `announce-list`, `comment`, `created by`, `url-list`, `httpseeds`, `nodes`, `publisher-url`, `azureus_properties` et toute clé inconnue. Un `.torrent` de plus de 8 Mio, imbriqué au-delà de 64 niveaux, illisible ou dont une clé de premier niveau apparaît deux fois est refusé, sans que l'erreur en cite le contenu.
- **Copie scellée** : le `.torrent` complet chiffré en AES-256-GCM avec `NECROMANCIARR_SECRET_KEY`. Un bloc vaut version (1 octet, valeur 1) ‖ nonce (12 octets) ‖ chiffré ‖ étiquette (16 octets) ; le nonce est tiré de `crypto/rand` à chaque scellement, un aléa défaillant fait échouer le scellement. La donnée associée est l'octet de version suivi de `infohash|tracker|source` (infohash en 40 caractères hexadécimaux minuscules, tracker éventuellement vide, source `provider` ou `torznab:<name>`) ; `|`, les caractères de contrôle et l'UTF-8 invalide sont refusés dans chaque composant, si bien que deux liaisons distinctes ne se confondent jamais ; l'octet de version précède cette donnée associée, donc une version modifiée échoue à l'authentification. Au descellement, la liaison est toujours reconstruite depuis la ligne du candidat (infohash, tracker et source tels qu'enregistrés au scellement), jamais depuis la configuration courante, et le tracker est comparé octet pour octet. Un bloc déplacé vers un autre candidat, tracker ou source, tronqué, altéré ou ouvert avec une autre clé donne une seule erreur, `sealed_torrent_unreadable`, qui ne dit pas laquelle de ces causes.
- **La clé** : 32 octets en base64url sans remplissage (43 caractères), tirés par `necromanciarr api-key --secret` qui l'affiche sur stdout seulement. Seule cette écriture est admise (ni base64 standard, ni hexadécimal, ni espace) ; une valeur invalide est une erreur d'usage qui ne nomme que la variable. La clé n'est jamais sérialisée et elle est masquée partout sous ses écritures probables (brute, base64, hexadécimal). Sa remise à zéro en mémoire n'est qu'un effort : Go ne garantit pas l'absence de copie.
- **Clé perdue ou changée** : les candidats restent lisibles (la copie assainie suffit à Verify), mais Seed refuse un candidat dont le bloc ne se descelle plus et `doctor` (`secret-key`) le signale en `sealed_torrent_unreadable`. Relancer Verify sur ce candidat récupère un `.torrent` neuf dans le budget de l'indexeur. Sauvegarder la clé avec le `.env`.

## Injection de l'annonce

Le `.torrent` du fournisseur n'a pas d'annonce, et la copie conservée d'un `.torrent` d'indexeur en est privée. Seed rend au client un `.torrent` construit en mémoire : la copie assainie de la colonne `torrent` (`info` recopié octet pour octet, drapeau `private` intact), plus `announce` et `announce-list`.

| Source (`announce_source`) | Condition | Annonce injectée |
|---|---|---|
| `config` | une entrée `trackers` couvre le tracker du candidat (clé ou alias, après normalisation) | celle de la configuration, qui remplace toute autre, même celle d'un indexeur |
| `indexer` | aucune entrée, mais un `.torrent` scellé de même infohash est en base | celle du `.torrent` scellé, descellé en mémoire avec la liaison de la ligne du candidat |
| `none` | ni l'une ni l'autre (tracker vide compris : seule la voie `indexer` reste) | aucune : `no_announce` |

- **`no_announce`** : la proposition porte l'avertissement « aucune annonce pour le tracker <nom> : ajouter une entrée `trackers` » et `seedable: false`. Les deux sont recalculés à chaque lecture (`proposals list`, `status`, Link, Seed) et jamais stockés : ajouter l'entrée `trackers` lève l'état sans revérifier. Accepter reste permis ; Link refuse la proposition (`no_announce`) avant toute sonde ou création de dossier, et Seed la refuse encore par défense. Un torrent public suit la même règle : pas de seed par DHT seul. L'interface affiche la pastille « Sans annonce », dont l'info-bulle dit que le lien sera refusé tant qu'une entrée `trackers` n'est pas ajoutée ; le bouton Accepter reste actif, comme dans la CLI. Un tracker vide est nommé « (inconnu) ».
- **Contrôles avant l'ajout** : un `.torrent` scellé qui ne se descelle plus donne `sealed_torrent_unreadable` (relancer Verify), un `.torrent` scellé sans `NECROMANCIARR_SECRET_KEY` donne `secret_key_missing`, et un infohash relu (après descellement, puis après injection) différent de celui du candidat donne `infohash_changed_after_inject`. Dans tous ces cas, rien n'est remis au client ; `secret_key_missing` est déjà refusé par Link, avant toute sonde ou création de dossier.
- **Mémoire seulement** : le `.torrent` injecté n'est passé qu'à l'ajout du client ; il n'est jamais écrit sur disque, en base, dans un résultat d'étape, un avertissement, une réponse ou un journal, et son tampon est effacé après l'ajout (au mieux : Go peut en avoir fait des copies). Le clair descellé est effacé dès l'annonce lue ; les valeurs de passkey qu'elle porte (segments de chemin, valeurs de paramètres) masquent toute erreur qui sort de l'étape. L'erreur du client à l'ajout n'est jamais relue : un serveur qui recopie le corps reçu dans sa réponse ne fait rien fuir.
- **cross-seed** : en mode `delegate`, la notification n'envoie que `{"infoHash": …}`. En mode `native`, chaque torrent d'un groupe résout sa propre annonce et peut être `no_announce` seul ; `accept-group` accepte quand même, Link et Seed traitent chaque proposition selon sa propre annonce. qBittorrent garde ensuite l'annonce dans son propre état, hors du contrôle de Necromanciarr.

| Donnée | Où | Durée |
|---|---|---|
| Annonce configurée | gabarit dans `config.json`, valeur dans l'environnement | — |
| `.torrent` d'indexeur complet | `torrent_sealed`, scellé | jusqu'à la décision, ou dès qu'une entrée `trackers` couvre le tracker (au démarrage et à chaque lecture) |
| `.torrent` injecté | nulle part | le temps de l'ajout |

Après avoir modifié `trackers`, redémarrer le service : un démon lancé avant la modification garde l'ancienne section, alors qu'une commande lancée avec la nouvelle efface déjà les `.torrent` scellés que l'entrée couvre ; le démon verrait alors un `no_announce` passager. À la lecture, cet effacement se fait au mieux : une base occupée le reporte (ligne de journal `sealed_clear_failed`) sans priver l'opérateur de ses propositions ; au démarrage, un échec arrête l'ouverture.

## Masquage des passkeys

Toute sortie (CLI texte et `--json`, API web, interface, `doctor`, journal du serveur, résultats de jobs, messages de panique) passe par le même masquage :

| Règle | Exemple masqué | Rendu |
|---|---|---|
| Secret connu (valeur interpolée dans `trackers` d'au moins 8 caractères, clés `torznab` et `prowlarr`, `NECROMANCIARR_SECRET_KEY`…), sous toutes ses formes | une passkey nue dans un message | `***` |
| URL d'annonce rendue par la configuration | `https://u:p@tracker-a.example.com:8443/abc/announce?pk=x` | `https://tracker-a.example.com:8443/***` |
| Paramètre à nom de passkey dans une URL, quelle que soit sa longueur (`passkey`, `pk`, `authkey`, `torrent_pass`, `key`, `uid`, `apikey`, `secret`, `token`, casse et encodage ignorés, séparateur `&` ou `;`, fragment compris) | `?pk=x` | `?pk=***` |
| Segment de chemin qui suit un tel nom | `/passkey/x/announce` | `/passkey/***/announce` |
| Segment ou valeur qui contient, une fois décodé (jusqu'à quatre fois : `%252F`), un mot hexadécimal ou base64url de 16 caractères ou plus | `/0000aaaa0000aaaa0000aaaa0000aaaa/announce` | `/***/announce` |
| Identifiants d'une URL | `https://alice:x@tracker-a.example.com/` | `https://***@tracker-a.example.com/` |
| Valeur de `trackers` de moins de 8 caractères : segment ou valeur égal à l'identique, quel que soit le nom du paramètre | `/42/announce?id=42` | `/***/announce?id=***` |
| Mot d'au moins 16 caractères avec un chiffre dans l'hôte (jeton en sous-domaine) | `https://0000aaaa0000aaaa0000aaaa0000aaaa.t.example/` | `https://***.t.example/` |
| URL échappée pour JSON (`https:\/\/…`, corps PHP `json_encode`) | une URL d'annonce dans un corps de réponse | `***` |

Limites acceptées : une passkey de moins de 16 caractères dans un chemin, ni après un nom de passkey ni valeur de `trackers`, reste visible ; une passkey en base64 standard (`+`, `/`) est coupée en morceaux de moins de 16 caractères et n'est masquée que si c'est un secret connu ; une URL sans `scheme://` n'est pas reconnue ; une URL coupée par une espace ou un saut de ligne n'est traitée que jusqu'à la coupure. Hors d'une URL, une passkey nue n'est masquée que si c'est un secret connu (8 caractères au moins) ; quand Seed lit l'annonce d'un `.torrent` d'indexeur, sa passkey rejoint les secrets connus le temps de l'étape : seules des valeurs y entrent (8 octets au moins pour le remplacement partout, plus courtes réservées aux URL), jamais des mots de chemin ordinaires comme `announce`. Un infohash placé dans une URL est masqué par la règle des 16 caractères (sans dommage : il n'est pas secret) ; seul, hors d'une URL, il reste affiché. Le masquage est en temps linéaire et une seconde passe ne change rien.

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
