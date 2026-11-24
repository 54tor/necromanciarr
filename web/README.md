# Interface web

Vue 3, Vite, TypeScript strict, Tailwind v4, shadcn-vue (Reka UI). Thème unique noir anthracite : jetons dans `:root`, aucune variante `dark:`, aucune bascule (vérifié par `src/single-theme.spec.ts`).

## Versions de Node

`engines` impose Node >= 22.12 (minimum de Vite 8, contrôlé par le Makefile). La CI et l'image Docker épinglent la version exacte 26.13.1 : changer les deux ensemble.

## Régénérer un composant shadcn-vue

`npx shadcn-vue@2.9.0 add <composant>`, puis corriger :

1. Les imports `from "cn"` deviennent `from "@/lib/utils"`.
2. Supprimer la dépendance `cn` ajoutée à tort à `package.json` (`npm uninstall cn`).
3. Retirer chaque utilitaire `dark:` en gardant sa valeur comme valeur de base (le test `single-theme.spec.ts` échoue sinon).
4. Épingler les versions ajoutées (`.npmrc` impose `save-exact`) et lancer `npx prettier --write .`.

## Marqueur de construction

`public/.placeholder` est copié par Vite dans `internal/web/ui/dist/` à chaque build ; c'est le seul fichier de `dist` suivi par git. Sans build, le binaire embarque ce marqueur et pas d'`index.html`.

## Licences et assets

`make ui-licences` (`scripts/check-licences.mjs`) : liste d'autorisation SPDX sur le lockfile (production et dev ; MPL-2.0 en dev seulement ; `(MIT OR GPL-3.0)` refusé, dérogation justifiée par paquet dans `licences-overrides.json`). Tout fichier de `src` ou `public` hors types texte (SVG compris) est un asset : il figure dans `assets-allowlist.json` avec `path`, `origin`, `licence` et `sha256`. Un nom de fichier contenant Radarr, Sonarr, Prowlarr, Servarr, Lidarr ou Readarr échoue ; le contenu ne l'est que pour du code copié (« Servarr », en-tête GPL, classe CSS ou chemin d'import à ce nom), les locales et specs étant exemptées. `dist` relève de `ui-csp`.

## Textes d'interface

Règle ESLint `no-raw-text` (texte et attributs `title`, `aria-label`, `alt`, `placeholder`) : aucun texte en dur. `lint-exceptions.json` liste les fichiers non encore migrés ; `lint-exceptions.spec.ts` échoue quand l'un n'en a plus besoin.

## Logo

Les logos sont l'identité visuelle PROPRE du projet, redimensionnée ; ce n'est ni un tiers ni un emprunt à une application *arr. Les originaux (`assets/branding/`, jamais modifiés) pèsent 0,9 à 2,4 Mo : l'interface n'embarque que des copies dérivées dans `public/branding/` (`logo-complet.webp` 288 px, barre latérale large ; `embleme.webp` 64 px, barre réduite et mobile ; `favicon-32.png` et `apple-touch-icon.png` 180 px, tirés de `icone.png`, pour `index.html`). Régénération : `web/scripts/derive-branding.sh` (ImageMagick 7, sRGB, métadonnées retirées, alpha conservé ; affiche les sha256). Chaque dérivé figure dans `assets-allowlist.json` (`make ui-licences`) ; un test refuse tout fichier de `public/branding` de plus de 100 Ko. Empreintes sha256 des originaux : `logo-complet.png` f60d52a5…23d4, `embleme.png` 4e9ef534…e516, `icone.png` 00fa37cc…3516 (empreintes complètes : `sha256sum assets/branding/*.png`).

## Interface

Navigation par hash (`#/demarrage`, `#/propositions`, `#/deduplication`, `#/liens`, `#/etapes`, `src/router.ts`) : le serveur sert `index.html` pour tout chemin sans extension, un routeur de chemins n'apporterait rien. Le jeton d'accès est saisi dans une boîte de dialogue après un 401 et reste dans une variable de module (`src/api/client.ts`) : jamais dans un stockage, une URL ni un cookie.

Coque (`AppShell.vue`) : barre latérale (`SidebarNav.vue`) complète dès 1024 px, réduite aux icônes de 768 à 1023 px (libellés en `sr-only`), tiroir (Reka UI `Dialog` : piège de focus, Échap, focus rendu au bouton) sous 768 px, via `useMediaQuery`. Lien d'évitement vers `main#main-content` (il déplace le focus sans toucher au hash du routeur). `PageHeader` porte le `h1` de la page, `PageToolbar` deux emplacements (`actions` à gauche, `filters` à droite) ; les boutons d'action de page portent `data-scope="toolbar"`. `App.vue` ne montre `#/sources` et `#/volumes` (déclarés dans `ROUTES`) que lorsqu'une vue existe. Les icônes viennent de `@lucide/vue` ; le mouvement respecte `prefers-reduced-motion` (`motion-reduce:`). Les tests simulent la largeur avec `src/test/viewport.ts` (`stubViewport`).

Tableau (`DataTable.vue`, générique, `@tanstack/vue-table` 9.3.1) : `<table>` natif avec `<caption>`, `scope="col"`, `aria-sort`, tri par bouton, pagination client (100 lignes) ou serveur (`serverPage`), flèches haut/bas entre les lignes (focus itinérant), Entrée émet `open`, sous 768 px les lignes deviennent des cartes (mêmes `data-action`, un libellé par champ). En v9, `ColumnDef` porte les fonctionnalités : utiliser `DataColumn<Row>` (`src/lib/dataTable.ts`). Les cases à cocher n'existent que via `selectable` et il n'y a aucune case « tout sélectionner » (jamais d'acceptation de masse). `StatusPill` : tons `ok|warn|fail|info|muted`, icône + libellé ; jetons `--state-*` vérifiés par `contrast.spec.ts`. `ConfirmDialog` : `summaryKey`, `items`, `confirmPhrase` (bouton désactivé tant que la phrase exacte n'est pas saisie).

Disposition : chaque ligne (proposition, remplacement, lien) est une carte empilée plutôt qu'une ligne de tableau, ce qui tient à 360 px sans défilement horizontal ni colonne figée ; les cartes se réorganisent en grille à partir de 640 px.

`src/api/actions.ts` recopie la table des routes du serveur et déclare la vue qui porte le bouton de chaque action ; `src/ui-parity.spec.ts` échoue quand une action n'a pas de bouton (`data-action`). Les textes de `Démarrage` venant du diagnostic du serveur (titres et détails des contrôles) restent dans la langue du serveur.

`Démarrage` et `Étapes` utilisent `DataTable` : les contrôles du diagnostic sont regroupés par famille (préfixe de l'identifiant, `src/lib/doctorFamily.ts` ; identifiant inconnu = « Autres »), un tableau par famille, avec le bouton `data-action="doctor run" data-scope="row"` pour relancer un contrôle ; les jobs sont triés par début décroissant et leur résumé vient de `src/lib/stageSummary.ts` (motifs : `reason_labels` du serveur).

## Limites v1

L'état des liens enregistrés est recalculé à chaque affichage (Lstat et Stat seulement, 20 s au plus, liste marquée partielle au-delà). Un reflink ou une copie n'est comparé que par sa taille (aucun contenu lu) ; sans taille enregistrée, il est « Conforme » avec la mention « Non comparé ». Un lien symbolique repointé vers une autre cible existante n'est pas détecté (comme dans l'analyse de réparation). Après une liste partielle, « Charger plus » réexamine tout : ses pages peuvent ne pas se raccorder exactement.
