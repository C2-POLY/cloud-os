import { parentOf, type Entry, type FoundBy, type SearchFilters } from '../drive/files.ts'

/** The type of file Demander's list keeps: everything, documents or photos and videos. */
export type Kind = '' | 'docs' | 'photos'

export const kinds: { value: Kind; label: string }[] = [
  { value: '', label: 'Tout' },
  { value: 'docs', label: 'Documents' },
  { value: 'photos', label: 'Photos' },
]

/** The period Demander's list keeps, by the date of each file: taken for a photo, else modified. */
export type Period = '' | 'month' | 'year' | 'last-year' | 'older'

export const periods: { value: Period; label: string }[] = [
  { value: '', label: 'Toutes les dates' },
  { value: 'month', label: '30 derniers jours' },
  { value: 'year', label: 'Cette année' },
  { value: 'last-year', label: "L'an dernier" },
  { value: 'older', label: 'Plus ancien' },
]

/** A day on the browser's wall clock, as the core reads it: YYYY-MM-DD. */
function dayOf(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/** The filters of the core for a kind and a period, the period counted from today. */
export function filtersOf(kind: Kind, period: Period, today: Date): SearchFilters {
  const f: SearchFilters = kind ? { type: kind } : {}
  const year = today.getFullYear()
  switch (period) {
    case 'month':
      return { ...f, from: dayOf(new Date(year, today.getMonth(), today.getDate() - 29)), to: dayOf(today) }
    case 'year':
      return { ...f, from: `${year}-01-01` }
    case 'last-year':
      return { ...f, from: `${year - 1}-01-01`, to: `${year - 1}-12-31` }
    case 'older':
      return { ...f, to: `${year - 2}-12-31` }
    default:
      return f
  }
}

const foundByLabels: Record<FoundBy, string> = {
  name: 'le nom',
  text: 'les mots du texte',
  prefix: "le début d'un mot du texte",
  fuzzy: 'un mot approchant du texte',
  meaning: 'le sens du texte',
  image: "ce que montre l'image",
}

/** How a file was found, in words: "le nom, le sens du texte". */
export function foundByText(found: FoundBy[]): string {
  return found.map((f) => foundByLabels[f]).join(', ')
}

/** The folder that shows an entry in Fichiers: itself for a folder, its parent for a file. */
export function folderOf(e: Entry): string {
  return e.kind === 'dir' ? e.path : parentOf(e.path)
}
