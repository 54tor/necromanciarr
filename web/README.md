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

## Logo

Pictogramme dessiné à la main en SVG inline dans `App.vue` ; aucun fichier externe.

## Interface

Navigation par hash (`#/demarrage`, `#/propositions`, `#/deduplication`, `#/liens`, `#/etapes`, `src/router.ts`) : le serveur sert `index.html` pour tout chemin sans extension, un routeur de chemins n'apporterait rien. Le jeton d'accès est saisi dans une boîte de dialogue après un 401 et reste dans une variable de module (`src/api/client.ts`) : jamais dans un stockage, une URL ni un cookie.

Disposition : chaque ligne (proposition, remplacement, lien) est une carte empilée plutôt qu'une ligne de tableau, ce qui tient à 360 px sans défilement horizontal ni colonne figée ; les cartes se réorganisent en grille à partir de 640 px.

`src/api/actions.ts` recopie la table des routes du serveur et déclare la vue qui porte le bouton de chaque action ; `src/ui-parity.spec.ts` échoue quand une action n'a pas de bouton (`data-action`). Les textes de `Démarrage` venant du diagnostic du serveur (titres et détails des contrôles) restent dans la langue du serveur.

## Limites v1

L'état des liens enregistrés est recalculé à chaque affichage (Lstat et Stat seulement, 20 s au plus, liste marquée partielle au-delà). Un reflink ou une copie n'est comparé que par sa taille (aucun contenu lu) ; sans taille enregistrée, il est « Conforme » avec la mention « Non comparé ». Un lien symbolique repointé vers une autre cible existante n'est pas détecté (comme dans l'analyse de réparation). Après une liste partielle, « Charger plus » réexamine tout : ses pages peuvent ne pas se raccorder exactement.
