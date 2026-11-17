import { api } from '../api.ts'
import { parentOf, type Entry } from '../drive/files.ts'
import { driveURL } from '../router.ts'

/** A run of events, as the core's `activity.Group` writes it. */
export interface ActivityGroup {
  day: string // YYYY-MM-DD in the browser's time zone
  kind: 'added' | 'replaced' | 'trashed' | 'restored' | 'share_opened'
  place: string // the folder, or the shared item's path
  name?: string // the one file's path when count is 1
  count: number
  last: string
}

export function fetchRecent(limit = 8): Promise<Entry[]> {
  return api<Entry[]>('GET', `/api/files/recent?limit=${limit}`)
}

export function fetchActivity(): Promise<ActivityGroup[]> {
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  return api<ActivityGroup[]>('GET', `/api/activity?days=7&tz=${encodeURIComponent(tz)}`)
}

const base = (path: string) => path.split('/').pop() || path
const folder = (place: string) => place || 'Mon drive'
const plural = (n: number, one: string, many: string) => `${n} ${n > 1 ? many : one}`

/** One line of "Ce qui s'est passé", and where a click on it goes. */
export function describe(g: ActivityGroup): { text: string; href: string } {
  const one = g.count === 1 && g.name ? base(g.name) : null
  switch (g.kind) {
    case 'added':
      return { text: one ? `${one} ajouté dans ${folder(g.place)}` : `${plural(g.count, 'fichier ajouté', 'fichiers ajoutés')} dans ${folder(g.place)}`, href: driveURL(g.place) }
    case 'replaced':
      return { text: one ? `Nouvelle version de ${one}, l'ancienne est gardée` : `${plural(g.count, 'fichier remplacé', 'fichiers remplacés')} dans ${folder(g.place)}, anciennes versions gardées`, href: driveURL(g.place) }
    case 'trashed':
      return { text: one ? `${one} mis à la corbeille` : `${plural(g.count, 'élément mis', 'éléments mis')} à la corbeille depuis ${folder(g.place)}`, href: '/corbeille' }
    case 'restored':
      return { text: one ? `${one} restauré dans ${folder(g.place)}` : `${plural(g.count, 'élément restauré', 'éléments restaurés')} dans ${folder(g.place)}`, href: driveURL(g.place) }
    case 'share_opened':
      return { text: `Lien « ${base(g.place)} » ouvert${g.count > 1 ? ` ${g.count} fois` : ''}`, href: '/partages' }
  }
}

function isoDay(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

const dayFormat = new Intl.DateTimeFormat('fr-FR', { weekday: 'long', day: 'numeric', month: 'long' })

/** "Aujourd'hui", "Hier", or the day's name and date. */
export function dayLabel(day: string, now = new Date()): string {
  if (day === isoDay(now)) return "Aujourd'hui"
  const yesterday = new Date(now)
  yesterday.setDate(now.getDate() - 1)
  if (day === isoDay(yesterday)) return 'Hier'
  const [y, m, d] = day.split('-').map(Number)
  const label = dayFormat.format(new Date(y, m - 1, d))
  return label.charAt(0).toUpperCase() + label.slice(1)
}

/** The folder a recent file opens in. */
export function recentHref(e: Entry): string {
  return driveURL(parentOf(e.path))
}
