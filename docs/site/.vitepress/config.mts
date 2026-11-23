import { defineConfig } from 'vitepress'

const base = '/necromanciarr/'

export default defineConfig({
  title: 'Necromanciarr',
  description: 'Retrouver et reconstituer des .torrent vérifiés depuis une bibliothèque Plex.',
  lang: 'fr',
  base,
  appearance: 'dark',
  cleanUrls: true,
  head: [['link', { rel: 'icon', type: 'image/png', href: `${base}logo-icone.png` }]],
  themeConfig: {
    logo: '/logo-embleme.png',
    nav: [
      { text: 'Accueil', link: '/' },
      { text: 'Installation', link: '/installation' },
      { text: 'Configuration', link: '/configuration' },
      { text: 'CLI', link: '/cli' },
      { text: 'Webhook', link: '/webhook' },
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
    ],
  },
})
