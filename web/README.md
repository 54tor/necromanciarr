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
