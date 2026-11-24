import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.spec.ts'],
    coverage: {
      provider: 'v8',
      include: ['src/**/*.{ts,vue}'],
      // Les composants shadcn générés et le point d'entrée ne portent pas de logique propre.
      exclude: ['src/**/*.spec.ts', 'src/components/ui/**', 'src/main.ts', 'src/vite-env.d.ts'],
      // Seuil bas tant que les vues de la tâche 9b n'existent pas ; à relever avec elles.
      thresholds: { lines: 80, functions: 80, branches: 70, statements: 80 },
    },
  },
})
