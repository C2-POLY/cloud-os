import { api } from '../api.ts'
import type { Entry, FoundBy, SearchAnswer, SearchHit, SnippetPart } from '../../../shared/api-types.ts'

export type { Entry, FoundBy, SearchAnswer, SearchHit, SnippetPart }

const q = (path: string) => encodeURIComponent(path)

/** What narrows Demander's list: a type of file, and days (YYYY-MM-DD, included, in the browser's time zone). */
export interface SearchFilters {
  type?: 'docs' | 'photos'
  from?: string
  to?: string
}

/**
 * Demander's one list: the account's files found by name, by the words of
 * their text, by its meaning and by what their photo shows, best first
 * (GET /api/search/all).
 */
export function searchAll(query: string, filters: SearchFilters = {}, limit = 30): Promise<SearchAnswer> {
  const params = new URLSearchParams({ q: query, limit: String(limit) })
  if (filters.type) params.set('type', filters.type)
  if (filters.from) params.set('from', filters.from)
  if (filters.to) params.set('to', filters.to)
  if (filters.from || filters.to) params.set('tz', Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC')
  return api<SearchAnswer>('GET', `/api/search/all?${params}`)
}

/** The direct children of a folder ("" is the root). */
export function listFolder(folder: string): Promise<Entry[]> {
  return api<Entry[]>('GET', `/api/files?path=${q(folder)}`)
}

export function createFolder(path: string): Promise<Entry> {
  return api<Entry>('POST', '/api/files/folders', { path })
}

export function moveEntry(from: string, to: string): Promise<Entry> {
  return api<Entry>('POST', '/api/files/move', { from, to })
}

/** The URL that downloads a file with the session cookie. */
export function downloadURL(path: string): string {
  return `/api/files/content?path=${q(path)}`
}

// Image and video types the workers make thumbnails of (core/internal/thumbs).
const thumbnailed = new Set(['image/jpeg', 'image/png', 'image/webp', 'image/heic', 'image/heif', 'video/mp4', 'video/quicktime'])

/** The thumbnail URL of an image or a video, keyed by its content so a new version is never stale; null for other files. */
export function thumbURL(entry: Entry): string | null {
  if (entry.kind !== 'file' || !entry.sha256 || !thumbnailed.has(entry.mime)) return null
  return `/api/files/thumb?path=${q(entry.path)}&v=${entry.sha256.slice(0, 16)}`
}

/** "a/b/c.txt" → "a/b"; "c.txt" → "". */
export function parentOf(path: string): string {
  const i = path.lastIndexOf('/')
  return i < 0 ? '' : path.slice(0, i)
}

/** Joins a folder and a name. */
export function join(folder: string, name: string): string {
  return folder ? `${folder}/${name}` : name
}

const units = ['o', 'Ko', 'Mo', 'Go', 'To']

/** A size in French units: 0 o, 12,4 Ko, 3,1 Go. */
export function formatSize(bytes: number): string {
  let n = bytes
  let u = 0
  while (n >= 1024 && u < units.length - 1) {
    n /= 1024
    u++
  }
  const digits = u === 0 || n >= 100 ? 0 : 1
  return `${n.toLocaleString('fr-FR', { maximumFractionDigits: digits })} ${units[u]}`
}

/** A date as "17 nov. 2026, 09:12". */
export function formatDate(iso: string): string {
  return new Date(iso).toLocaleString('fr-FR', { dateStyle: 'medium', timeStyle: 'short' })
}

/** A date to the second, "17 nov. 2026, 09:12:05", to tell close versions apart. */
export function formatDateSeconds(iso: string): string {
  return new Date(iso).toLocaleString('fr-FR', { dateStyle: 'medium', timeStyle: 'medium' })
}

export type SortKey = 'name' | 'size' | 'modified'

/** Folders first, then the chosen column; names compare as people read them ("2" before "10"). */
export function sortEntries(entries: Entry[], key: SortKey, ascending: boolean): Entry[] {
  const collator = new Intl.Collator('fr', { numeric: true, sensitivity: 'base' })
  const dir = ascending ? 1 : -1
  return [...entries].sort((a, b) => {
    if (a.kind !== b.kind) return a.kind === 'dir' ? -1 : 1
    let c = 0
    if (key === 'size') c = a.size - b.size
    if (key === 'modified') c = Date.parse(a.modified_at) - Date.parse(b.modified_at)
    if (c === 0) c = collator.compare(a.name, b.name)
    return c * dir
  })
}

/** One item of the trash, as the core's `httpapi.TrashEntry` writes it. */
export interface TrashEntry {
  id: number
  path: string // where it was
  name: string
  kind: 'file' | 'dir'
  size: number
  deleted_at: string
  expires_at: string
  /** Why the box put it there itself; absent for a deletion. */
  reason?: 'smb_not_saved'
}

/** What a trash item's reason says, for the one the box put there itself. */
export const trashReasons: Record<NonNullable<TrashEntry['reason']>, string> = {
  smb_not_saved: 'Gardé : l’enregistrement en SMB a échoué',
}

export function trashEntry(path: string): Promise<TrashEntry> {
  return api<TrashEntry>('POST', '/api/files/trash', { path })
}

export function listTrash(): Promise<TrashEntry[]> {
  return api<TrashEntry[]>('GET', '/api/trash')
}

export function restoreTrash(id: number): Promise<Entry> {
  return api<Entry>('POST', `/api/trash/${id}/restore`, {})
}

/** Erases one trash item for good; the core requires the confirmation. */
export function purgeTrash(id: number): Promise<void> {
  return api<void>('POST', `/api/trash/${id}/purge`, { confirm: true })
}

/** Erases the whole trash for good. */
export function emptyTrash(): Promise<{ purged: number }> {
  return api<{ purged: number }>('POST', '/api/trash/empty', { confirm: true })
}

/** "dans 12 jours", "demain", "aujourd'hui". */
export function inDays(iso: string, now = Date.now()): string {
  const days = Math.ceil((Date.parse(iso) - now) / 86_400_000)
  if (days <= 0) return "aujourd'hui"
  if (days === 1) return 'demain'
  return `dans ${days} jours`
}

/** A previous content of a file, as the core's `httpapi.VersionEntry` writes it. */
export interface VersionEntry {
  id: number
  size: number
  mime: string
  sha256: string
  modified_at: string
  replaced_at: string
}

export function listVersions(path: string): Promise<VersionEntry[]> {
  return api<VersionEntry[]>('GET', `/api/files/versions?path=${encodeURIComponent(path)}`)
}

/** The URL that downloads a version under the file's name. */
export function versionURL(id: number, name: string): string {
  return `/api/files/versions/${id}/content?name=${encodeURIComponent(name)}`
}

/** Makes a version the file's content again; the current content becomes a version. */
export function restoreVersion(id: number): Promise<Entry> {
  return api<Entry>('POST', `/api/files/versions/${id}/restore`, {})
}
