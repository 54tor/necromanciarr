import type { Component } from 'vue'

/** Entrée de la barre latérale : `fragment` est le chemin du routeur (`/liens`), jamais traduit. */
export interface NavItem {
  fragment: string
  /** Clé de libellé dans les fichiers de langue (`nav.liens`). */
  labelKey: string
  icon: Component
  /** Compte affiché en pastille ; 0 ou absent : aucune pastille. */
  badge?: number
}
