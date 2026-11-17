import { api } from '../api.ts'
import type { Entry } from '../drive/files.ts'

/** One link in its owner's list, as the core's `httpapi.ShareEntry` writes it. */
export interface ShareEntry {
  id: number
  path: string // where the item is now
  name: string
  kind: 'file' | 'dir'
  size: number
  has_password: boolean
  expires_at: string
  created_at: string
  expired: boolean
  trashed: boolean
}

/** What a guest sees of the shared item, as the core's `httpapi.GuestItem` writes it. */
export interface GuestItem {
  name: string
  kind: 'file' | 'dir'
  size: number
  mime: string
  modified_at: string
  expires_at: string
}

/** Creates a link; the token comes back only here, never again. */
export function createShare(path: string, expiresAt: Date, password: string): Promise<{ share: ShareEntry; token: string }> {
  return api('POST', '/api/shares', { path, expires_at: expiresAt.toISOString(), password })
}

export function listShares(): Promise<ShareEntry[]> {
  return api<ShareEntry[]>('GET', '/api/shares')
}

export function revokeShare(id: number): Promise<void> {
  return api<void>('DELETE', `/api/shares/${id}`, {})
}

/** The address a guest opens. */
export function shareURL(token: string, origin = window.location.origin): string {
  return `${origin}/s/${token}`
}

/** The token of a guest page address, or null. */
export function tokenFromURL(pathname: string): string | null {
  const m = /^\/s\/([A-Za-z0-9_-]+)\/?$/.exec(pathname)
  return m ? m[1] : null
}

const base = (token: string) => `/api/s/${encodeURIComponent(token)}`
const q = (path: string) => encodeURIComponent(path)

export function guestInfo(token: string): Promise<GuestItem> {
  return api<GuestItem>('GET', base(token))
}

export function guestUnlock(token: string, password: string): Promise<void> {
  return api<void>('POST', `${base(token)}/unlock`, { password })
}

/** A folder of the shared subtree; paths are relative to the shared item. */
export function guestList(token: string, path: string): Promise<Entry[]> {
  return api<Entry[]>('GET', `${base(token)}/files?path=${q(path)}`)
}

export function guestContentURL(token: string, path: string): string {
  return `${base(token)}/content?path=${q(path)}`
}

export function guestZipURL(token: string, path: string): string {
  return `${base(token)}/zip?path=${q(path)}`
}

const DAY = 86_400_000

/** The expiry of a quick choice ("1", "7", "30" days from now), or the end of a chosen day (YYYY-MM-DD, local time). */
export function expiryOf(choice: string, date: string, now = Date.now()): Date | null {
  if (choice === 'date') {
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date)
    if (!m) return null
    return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]), 23, 59, 59)
  }
  return new Date(now + Number(choice) * DAY)
}
