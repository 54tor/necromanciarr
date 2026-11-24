import { h } from 'vue'

/** Cellule vide d'un tableau : une valeur absente se lit « — », jamais comme une cellule oubliée. */
export const EMPTY_CELL = '—'

const WRAP_CLASS = 'whitespace-normal [overflow-wrap:anywhere]'

/** Un chemin se coupe de préférence après une barre oblique (`<wbr>`), sans changer son texte. */
export function pathCell(path: string) {
  const segments = path.split(/(?<=\/)/)
  return h(
    'span',
    { class: WRAP_CLASS },
    segments.flatMap((segment, position) => (position === 0 ? [segment] : [h('wbr'), segment])),
  )
}

/** Texte qui passe à la ligne au lieu de déborder de sa colonne. */
export const wrapCell = (text: string) => h('span', { class: WRAP_CLASS }, text)
