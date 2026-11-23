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
    nav: [{ text: 'Accueil', link: '/' }],
  },
})
