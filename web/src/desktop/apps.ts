import { HardDrive, Image, Share2, Trash2, Users, type LucideIcon } from 'lucide-react'
import { folderFromURL } from '../router.ts'

/** The address of Fichiers' Chronologie view. */
export const timelinePath = '/chronologie'

export type AppId = 'files' | 'photos' | 'shares' | 'trash' | 'accounts'

/** An app of the desktop: it opens in a window, at its own addresses. */
export type AppInfo = {
  id: AppId
  title: string
  icon: LucideIcon
  /** The dock tile's color. */
  color: string
  /** Where it opens when it is not open yet. */
  home: string
  adminOnly?: boolean
}

export const apps: readonly AppInfo[] = [
  { id: 'files', title: 'Fichiers', icon: HardDrive, color: '#2f6fdf', home: '/drive' },
  { id: 'photos', title: 'Photos', icon: Image, color: '#e8862a', home: '/photos' },
  { id: 'shares', title: 'Partages', icon: Share2, color: '#13996b', home: '/partages' },
  { id: 'trash', title: 'Corbeille', icon: Trash2, color: '#6b6f7a', home: '/corbeille' },
  { id: 'accounts', title: 'Comptes', icon: Users, color: '#7c4ddb', home: '/comptes', adminOnly: true },
]

export function appInfo(id: AppId): AppInfo {
  return apps.find((a) => a.id === id)!
}

/** The app an address belongs to; null for the desktop itself and unknown addresses. */
export function appOf(path: string): AppId | null {
  if (folderFromURL(path) !== null || path === timelinePath) return 'files'
  if (path === '/photos' || path === '/photos/albums' || /^\/photos\/albums\/\d+\/?$/.test(path)) return 'photos'
  if (path === '/partages') return 'shares'
  if (path === '/corbeille') return 'trash'
  if (path === '/comptes') return 'accounts'
  return null
}
