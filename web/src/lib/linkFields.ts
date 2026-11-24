import type { LinkItem } from '@/api/types'

/** Le serveur n'envoie pas encore de volume (phase C) : la colonne n'existe que si au moins un lien en porte un. */
export function hasVolumeField(links: LinkItem[]): boolean {
  return links.some((link) => link.volume !== undefined)
}
