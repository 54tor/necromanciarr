import { fileURLToPath, URL } from 'node:url'
import tailwindcss from '@tailwindcss/vite'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  // Chemins relatifs : le binaire Go sert les ressources depuis n'importe quel préfixe.
  base: './',
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  build: {
    // Dossier embarqué par internal/web/ui/embed.go ; emptyOutDir ne vide que ce dossier.
    outDir: '../internal/web/ui/dist',
    emptyOutDir: true,
  },
})
