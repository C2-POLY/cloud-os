import type { Entry, TimelineEntry } from '../../../shared/api-types'

/** "2,7 Ko", "1,2 Mo". */
export function formatSize(bytes: number): string {
  const units = ['o', 'Ko', 'Mo', 'Go', 'To']
  let n = bytes
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  const value = i === 0 ? String(n) : n.toFixed(n < 10 ? 1 : 0).replace('.', ',')
  return `${value} ${units[i]}`
}

const monthName = new Intl.DateTimeFormat('fr-FR', { month: 'long', year: 'numeric' })
const fullDate = new Intl.DateTimeFormat('fr-FR', { day: 'numeric', month: 'long', year: 'numeric', hour: '2-digit', minute: '2-digit' })

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)

/** "Juillet 2024" from "2024-07". */
export function monthLabel(month: string): string {
  const [y, m] = month.split('-').map(Number)
  return cap(monthName.format(new Date(y, m - 1, 1)))
}

/** A wall-clock date "2024-07-14T18:03:22" written out, without any time zone shift. */
export function wallClock(at: string): string {
  const [d, t = '00:00'] = at.split('T')
  const [y, m, day] = d.split('-').map(Number)
  const [h, min] = t.split(':').map(Number)
  return fullDate.format(new Date(y, m - 1, day, h, min))
}

/** Folders first, then by name, as the web drive sorts by default. */
export function sortEntries(entries: Entry[]): Entry[] {
  return entries.slice().sort((a, b) => (a.kind === b.kind ? a.name.localeCompare(b.name, 'fr', { numeric: true }) : a.kind === 'dir' ? -1 : 1))
}

/** Splits items, newest first, into months, keeping their order. */
export function byMonth(items: TimelineEntry[]): { month: string; items: TimelineEntry[] }[] {
  const out: { month: string; items: TimelineEntry[] }[] = []
  for (const it of items) {
    const month = it.at.slice(0, 7)
    const last = out.at(-1)
    if (last && last.month === month) last.items.push(it)
    else out.push({ month, items: [it] })
  }
  return out
}

/** The parent folder of a path ("" for the root). */
export function parentOf(path: string): string {
  const i = path.lastIndexOf('/')
  return i < 0 ? '' : path.slice(0, i)
}
