import { fileURLToPath, URL } from 'node:url'
import VueI18nPlugin from '@intlify/unplugin-vue-i18n/vite'
import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [
    vue(),
    tailwindcss(),
    // Messages précompilés au build et build runtime de vue-i18n : le compilateur de messages (qui exigerait
    // `unsafe-eval`) n'est jamais embarqué ; la CSP du serveur l'interdit.
    VueI18nPlugin({
      include: fileURLToPath(new URL('./src/locales/*.json', import.meta.url)),
      runtimeOnly: true,
      compositionOnly: true,
      strictMessage: true,
    }),
  ],
  // Chemins absolus : le serveur Go renvoie index.html pour toute route du client (/proposals/12) ; des chemins
  // relatifs y chercheraient /proposals/assets/... L'interface est servie à la racine.
  base: '/',
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    // Dossier embarqué par internal/web/ui/embed.go ; emptyOutDir ne vide que ce dossier.
    outDir: '../internal/web/ui/dist',
    emptyOutDir: true,
  },
})
