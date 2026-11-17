import { api } from '../api.ts'
import type { Entry } from '../drive/files.ts'

/** A file on the timeline, as the core's `httpapi.TimelineEntry` writes it. */
export interface TimelineEntry extends Entry {
  at: string // wall-clock date, "2024-07-14T18:03:22", no zone
  taken: boolean // at is when the photo was taken
}

export interface TimelineCursor {
  at: string
  path: string
}

export interface TimelinePage {
  items: TimelineEntry[]
  next: TimelineCursor | null
}

export interface MonthCount {
  month: string // "2026-11"
  count: number
}

const zone = () => encodeURIComponent(Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC')

export function fetchTimeline(before: TimelineCursor | null, limit = 60): Promise<TimelinePage> {
  const cursor = before ? `&before_at=${encodeURIComponent(before.at)}&before_path=${encodeURIComponent(before.path)}` : ''
  return api<TimelinePage>('GET', `/api/timeline?tz=${zone()}&limit=${limit}${cursor}`)
}

export function fetchMonths(): Promise<MonthCount[]> {
  return api<MonthCount[]>('GET', `/api/timeline/months?tz=${zone()}`)
}

/** The cursor that starts the timeline at the first day of the month after `month`: the month and older. */
export function startOfMonthAfter(month: string): TimelineCursor {
  const [y, m] = month.split('-').map(Number)
  const next = m === 12 ? `${y + 1}-01` : `${y}-${String(m + 1).padStart(2, '0')}`
  return { at: `${next}-01T00:00:00`, path: '' }
}

const monthName = new Intl.DateTimeFormat('fr-FR', { month: 'long', year: 'numeric' })

/** "Novembre 2026". */
export function monthLabel(month: string): string {
  const [y, m] = month.split('-').map(Number)
  const label = monthName.format(new Date(y, m - 1, 1))
  return label.charAt(0).toUpperCase() + label.slice(1)
}

export function isMedia(e: Entry): boolean {
  return e.mime.startsWith('image/') || e.mime.startsWith('video/')
}

/** Groups items, already newest first, by month, keeping that order. */
export function byMonth(items: TimelineEntry[]): { month: string; media: TimelineEntry[]; docs: TimelineEntry[] }[] {
  const out: { month: string; media: TimelineEntry[]; docs: TimelineEntry[] }[] = []
  for (const it of items) {
    const month = it.at.slice(0, 7)
    let group = out.at(-1)
    if (!group || group.month !== month) {
      group = { month, media: [], docs: [] }
      out.push(group)
    }
    ;(isMedia(it) ? group.media : group.docs).push(it)
  }
  return out
}

/** Every month from the first to the last one counted, the empty ones at 0, so the strip follows time. */
export function fillMonths(months: MonthCount[]): MonthCount[] {
  if (months.length === 0) return []
  const counts = new Map(months.map((m) => [m.month, m.count]))
  const [y0, m0] = months[0].month.split('-').map(Number)
  const last = months.at(-1)!.month
  const out: MonthCount[] = []
  for (let y = y0, m = m0; out.length < 1200; m === 12 ? ((y += 1), (m = 1)) : (m += 1)) {
    const month = `${y}-${String(m).padStart(2, '0')}`
    out.push({ month, count: counts.get(month) ?? 0 })
    if (month === last) break
  }
  return out
}
