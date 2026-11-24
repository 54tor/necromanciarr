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
      exclude: ['src/**/*.spec.ts', 'src/components/ui/**', 'src/test/**', 'src/main.ts', 'src/vite-env.d.ts'],
      // Relevés avec les vues de la tâche 9b (mesure : ~96 % lignes, ~93 % branches).
      thresholds: { lines: 90, functions: 90, branches: 85, statements: 90 },
    },
  },
})
