import { Briefcase, Folder, HeartPulse, IdCard, Landmark, PiggyBank, Receipt, Signature, type LucideIcon } from 'lucide-react'
import { api } from '../api.ts'
import type { CategoriesSummary, CategoryId, DocumentCursor, DocumentEntry, DocumentPage } from '../../../shared/api-types.ts'

export type { CategoriesSummary, CategoryId, DocumentCursor, DocumentEntry, DocumentPage }

/** The address of the app Documents. */
export const documentsPath = '/documents'

/** A category as the app shows it. */
export type Category = { id: CategoryId; label: string; icon: LucideIcon }

/** The categories, in the order of the core's `doccat.Categories`. */
export const categories: readonly Category[] = [
  { id: 'factures', label: 'Factures', icon: Receipt },
  { id: 'contrats', label: 'Contrats', icon: Signature },
  { id: 'impots', label: 'Impôts', icon: Landmark },
  { id: 'sante', label: 'Santé', icon: HeartPulse },
  { id: 'banque', label: 'Banque', icon: PiggyBank },
  { id: 'travail', label: 'Travail', icon: Briefcase },
  { id: 'administratif', label: 'Administratif', icon: IdCard },
  { id: 'autre', label: 'Autre', icon: Folder },
]

export function categoryOf(id: CategoryId): Category {
  return categories.find((c) => c.id === id)!
}

/** The address of a category in the app. */
export function categoryURL(id: CategoryId): string {
  return `${documentsPath}/${id}`
}

/**
 * What an address of the app shows: undefined for /documents itself (the
 * first category with documents), a category, or null when the address is
 * not the app's.
 */
export function categoryFromURL(path: string): CategoryId | undefined | null {
  if (path === documentsPath || path === documentsPath + '/') return undefined
  const id = path.startsWith(documentsPath + '/') ? path.slice(documentsPath.length + 1).replace(/\/$/, '') : ''
  return categories.some((c) => c.id === id) ? (id as CategoryId) : null
}

/** The number of the account's documents in each category. */
export function fetchCategories(): Promise<CategoriesSummary> {
  return api<CategoriesSummary>('GET', '/api/documents/categories')
}

/** A page of the account's documents in a category, from a cursor (null: the first page). */
export function fetchDocuments(category: CategoryId, after: DocumentCursor | null, limit = 60): Promise<DocumentPage> {
  const q = new URLSearchParams({ category, limit: String(limit) })
  if (after) {
    q.set('before_at', after.at)
    q.set('before_path', after.path)
  }
  return api<DocumentPage>('GET', `/api/documents?${q}`)
}

/** Records the member's category for a document, or null to go back to the automatic one. */
export function setCategory(path: string, category: CategoryId | null): Promise<DocumentEntry> {
  return api<DocumentEntry>('PUT', '/api/documents/category', { path, category })
}
