import { fileURLToPath, URL } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
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
