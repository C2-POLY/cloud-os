import { api } from '../api.ts'
import type { Album, TimelineCursor, TimelinePage } from '../../../shared/api-types.ts'

export type { Album }

/** Photos' views: the timeline, the list of albums, one album. */
export const photosPath = '/photos'
export const albumsPath = '/photos/albums'

export function albumURL(id: number): string {
  return `${albumsPath}/${id}`
}

/** The album an address shows, or null. */
export function albumFromURL(path: string): number | null {
  const m = /^\/photos\/albums\/(\d+)\/?$/.exec(path)
  return m ? Number(m[1]) : null
}

const zone = () => encodeURIComponent(Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC')

export const listAlbums = () => api<Album[]>('GET', '/api/albums')
export const getAlbum = (id: number) => api<Album>('GET', `/api/albums/${id}`)
export const createAlbum = (name: string) => api<Album>('POST', '/api/albums', { name })
export const renameAlbum = (id: number, name: string) => api<Album>('PATCH', `/api/albums/${id}`, { name })
/** Deletes the album, never its photos. */
export const deleteAlbum = (id: number) => api<void>('DELETE', `/api/albums/${id}`, {})
export const addToAlbum = (id: number, paths: string[]) => api<Album>('POST', `/api/albums/${id}/items`, { paths })
/** Takes photos out of the album; they stay in the drive. */
export const removeFromAlbum = (id: number, paths: string[]) => api<Album>('POST', `/api/albums/${id}/items/remove`, { paths })

export function fetchAlbumItems(id: number, before: TimelineCursor | null, limit = 60): Promise<TimelinePage> {
  const cursor = before ? `&before_at=${encodeURIComponent(before.at)}&before_path=${encodeURIComponent(before.path)}` : ''
  return api<TimelinePage>('GET', `/api/albums/${id}/items?tz=${zone()}&limit=${limit}${cursor}`)
}
