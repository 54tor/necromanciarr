import { defineConfig } from 'vitepress'

const base = '/necromanciarr/'

export default defineConfig({
  title: 'Necromanciarr',
  description: 'Retrouver et reconstituer des .torrent vérifiés depuis une bibliothèque Plex.',
  lang: 'fr',
  base,
  appearance: 'dark',
  cleanUrls: true,
  head: [
    ['link', { rel: 'icon', type: 'image/png', sizes: '64x64', href: `${base}favicon.png` }],
    ['link', { rel: 'apple-touch-icon', href: `${base}apple-touch-icon.png` }],
  ],
  themeConfig: {
    logo: '/logo-embleme.png',
    nav: [
      { text: 'Accueil', link: '/' },
      { text: 'Installation', link: '/installation' },
      { text: 'Configuration', link: '/configuration' },
      { text: 'CLI', link: '/cli' },
      { text: 'Webhook', link: '/webhook' },
      { text: 'API', link: '/api' },
    ],
    sidebar: [
      {
        text: 'Utilisation',
        items: [
          { text: 'Installation', link: '/installation' },
          { text: 'Configuration', link: '/configuration' },
          { text: 'Ligne de commande', link: '/cli' },
          { text: 'Webhook', link: '/webhook' },
        ],
      },
      { text: 'Référence', items: [{ text: 'API', link: '/api' }] },
    ],
  },
})
