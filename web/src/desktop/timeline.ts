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

/** Narrows the timeline: images and videos only (the Photos app), or everything. */
export type TimelineFilter = 'media' | undefined

const only = (filter: TimelineFilter) => (filter ? `&filter=${filter}` : '')

export function fetchTimeline(before: TimelineCursor | null, limit = 60, filter?: TimelineFilter): Promise<TimelinePage> {
  const cursor = before ? `&before_at=${encodeURIComponent(before.at)}&before_path=${encodeURIComponent(before.path)}` : ''
  return api<TimelinePage>('GET', `/api/timeline?tz=${zone()}&limit=${limit}${only(filter)}${cursor}`)
}

export function fetchMonths(filter?: TimelineFilter): Promise<MonthCount[]> {
  return api<MonthCount[]>('GET', `/api/timeline/months?tz=${zone()}${only(filter)}`)
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

/** Splits items, newest first, into their days, keeping that order. */
export function byDay(items: TimelineEntry[]): { day: string; items: TimelineEntry[] }[] {
  const out: { day: string; items: TimelineEntry[] }[] = []
  for (const it of items) {
    const day = it.at.slice(0, 10)
    const group = out.at(-1)
    if (group && group.day === day) group.items.push(it)
    else out.push({ day, items: [it] })
  }
  return out
}

const dayName = new Intl.DateTimeFormat('fr-FR', { weekday: 'long', day: 'numeric', month: 'long' })

/** "Dimanche 14 juillet". */
export function dayTitle(day: string): string {
  const [y, m, d] = day.split('-').map(Number)
  const label = dayName.format(new Date(y, m - 1, d))
  return label.charAt(0).toUpperCase() + label.slice(1)
}

/** A photo's metadata, as GET /api/files/meta returns it (docs/architecture/metadata.md). */
export interface MediaMeta {
  taken_at: string | null
  taken_offset: string | null
  latitude: number | null
  longitude: number | null
  make: string | null
  model: string | null
  lens: string | null
  width: number | null
  height: number | null
}

const when = new Intl.DateTimeFormat('fr-FR', { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric', hour: '2-digit', minute: '2-digit' })

/** A wall-clock date "2024-07-14T18:03:22" written out, without any time zone conversion. */
export function formatWallClock(at: string): string {
  const [d, t = '00:00:00'] = at.split('T')
  const [y, m, day] = d.split('-').map(Number)
  const [h, min] = t.split(':').map(Number)
  const label = when.format(new Date(y, m - 1, day, h, min))
  return label.charAt(0).toUpperCase() + label.slice(1)
}
