import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { appOf } from '../desktop/apps.ts'
import { mockApi } from '../test-api.ts'
import { DocumentsPage } from './Documents.tsx'
import { categories, categoryFromURL, type CategoriesSummary, type CategoryId, type DocumentEntry } from './documents.ts'

const doc = (path: string, category: CategoryId, more: Partial<DocumentEntry> = {}): DocumentEntry => ({
  path,
  name: path.split('/').pop()!,
  kind: 'file',
  size: 2048,
  mime: 'application/pdf',
  sha256: 'ab'.repeat(32),
  modified_at: '2024-04-04T10:00:00Z',
  category,
  auto: category,
  corrected: false,
  ...more,
})
const edf = doc('Factures/edf.pdf', 'factures')
const gaz = doc('Factures/gaz.pdf', 'factures')
const bail = doc('Logement/bail.docx', 'contrats', { mime: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document' })

const summary = (counts: Partial<Record<CategoryId, number>>, pending = 0): CategoriesSummary => ({
  categories: categories.map((c) => ({ id: c.id, count: counts[c.id] ?? 0 })),
  pending,
})
const page = (category: CategoryId) => `GET /api/documents?category=${category}&limit=60`

beforeEach(() => window.history.replaceState(null, '', '/documents'))
afterEach(() => vi.unstubAllGlobals())

describe('Documents addresses', () => {
  it('belong to the app Documents', () => {
    expect(appOf('/documents')).toBe('documents')
    expect(appOf('/documents/factures')).toBe('documents')
    expect(appOf('/documents/inconnue')).toBeNull()
    expect(categoryFromURL('/documents')).toBeUndefined()
    expect(categoryFromURL('/documents/impots')).toBe('impots')
    expect(categoryFromURL('/drive')).toBeNull()
  })
})

describe('Documents', () => {
  it('shows the categories with their counts, and the first one with documents', async () => {
    mockApi({
      'GET /api/documents/categories': { status: 200, body: summary({ contrats: 1, sante: 3 }, 2) },
      [page('contrats')]: { status: 200, body: { items: [bail], next: null } },
    })
    render(<DocumentsPage />)
    expect(await screen.findByRole('heading', { name: 'Contrats' })).toBeInTheDocument()
    const nav = screen.getByRole('navigation', { name: 'Catégories' })
    expect(within(nav).getAllByRole('link').map((a) => a.textContent)).toEqual([
      'Factures0',
      'Contrats1',
      'Impôts0',
      'Santé3',
      'Banque0',
      'Travail0',
      'Administratif0',
      'Autre0',
    ])
    expect(within(nav).getByRole('link', { name: /Contrats/ })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByText(/2 documents en cours de classement/)).toBeInTheDocument()
    expect(await screen.findByText('bail.docx')).toBeInTheDocument()
    expect(screen.getByText('Dans Logement')).toBeInTheDocument()

    fireEvent.click(within(nav).getByRole('link', { name: /Santé/ }))
    expect(window.location.pathname).toBe('/documents/sante')
  })

  it('stays on the first category it picked when corrections empty it', async () => {
    mockApi({
      'GET /api/documents/categories': [
        { status: 200, body: summary({ factures: 1, contrats: 1 }) },
        { status: 200, body: summary({ contrats: 2 }) },
      ],
      [page('factures')]: { status: 200, body: { items: [edf], next: null } },
      'PUT /api/documents/category': { status: 200, body: { ...edf, category: 'contrats', corrected: true } },
    })
    render(<DocumentsPage />)
    fireEvent.change(await screen.findByRole('combobox', { name: 'Catégorie de edf.pdf' }), { target: { value: 'contrats' } })
    expect(await screen.findByText('Aucun document dans Factures.')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Factures' })).toBeInTheDocument()
  })

  it('says when a category is empty', async () => {
    mockApi({
      'GET /api/documents/categories': { status: 200, body: summary({ factures: 2 }) },
      [page('banque')]: { status: 200, body: { items: [], next: null } },
    })
    render(<DocumentsPage category="banque" />)
    expect(await screen.findByText('Aucun document dans Banque.')).toBeInTheDocument()
  })

  it('corrects a category, and undoes it', async () => {
    const calls = mockApi({
      'GET /api/documents/categories': [
        { status: 200, body: summary({ factures: 2 }) },
        { status: 200, body: summary({ factures: 1, contrats: 1 }) },
        { status: 200, body: summary({ factures: 2 }) },
      ],
      [page('factures')]: [
        { status: 200, body: { items: [edf, gaz], next: null } },
        { status: 200, body: { items: [edf, gaz], next: null } },
      ],
      'PUT /api/documents/category': [
        { status: 200, body: { ...edf, category: 'contrats', corrected: true } },
        { status: 200, body: edf },
      ],
    })
    render(<DocumentsPage category="factures" />)
    const select = await screen.findByRole('combobox', { name: 'Catégorie de edf.pdf' })
    expect(select).toHaveValue('')
    expect(within(select).getAllByRole('option')[0]).toHaveTextContent('Factures (automatique)')

    fireEvent.change(select, { target: { value: 'contrats' } })
    expect(await screen.findByRole('status')).toHaveTextContent('« edf.pdf » classé dans Contrats.')
    expect(calls.find((c) => c.key === 'PUT /api/documents/category')?.body).toEqual({ path: 'Factures/edf.pdf', category: 'contrats' })
    expect(screen.queryByText('edf.pdf')).not.toBeInTheDocument()
    await waitFor(() => expect(within(screen.getByRole('navigation', { name: 'Catégories' })).getByRole('link', { name: /Contrats/ })).toHaveTextContent('Contrats1'))

    // Annuler gives the automatic category back (null), and the list comes back.
    fireEvent.click(screen.getByRole('button', { name: 'Annuler' }))
    expect(await screen.findByText('edf.pdf')).toBeInTheDocument()
    expect(calls.filter((c) => c.key === 'PUT /api/documents/category').at(-1)?.body).toEqual({ path: 'Factures/edf.pdf', category: null })
  })

  it('shows a corrected document with its choice, and can give it back to the box', async () => {
    const chosen = { ...bail, category: 'contrats' as const, auto: 'autre' as const, corrected: true }
    const calls = mockApi({
      'GET /api/documents/categories': { status: 200, body: summary({ contrats: 1 }) },
      [page('contrats')]: { status: 200, body: { items: [chosen], next: null } },
      'PUT /api/documents/category': { status: 200, body: { ...chosen, category: 'autre', corrected: false } },
    })
    render(<DocumentsPage category="contrats" />)
    const select = await screen.findByRole('combobox', { name: 'Catégorie de bail.docx' })
    expect(select).toHaveValue('contrats')
    expect(within(select).getAllByRole('option')[0]).toHaveTextContent('Autre (automatique)')
    fireEvent.change(select, { target: { value: '' } })
    expect(await screen.findByRole('status')).toHaveTextContent('« bail.docx » classé dans Autre.')
    expect(calls.find((c) => c.key === 'PUT /api/documents/category')?.body).toEqual({ path: 'Logement/bail.docx', category: null })
  })

  it('loads the next page from the cursor', async () => {
    mockApi({
      'GET /api/documents/categories': { status: 200, body: summary({ factures: 2 }) },
      [page('factures')]: { status: 200, body: { items: [gaz], next: { at: '2024-04-04T10:00:00.123456Z', path: 'Factures/gaz.pdf' } } },
      'GET /api/documents?category=factures&limit=60&before_at=2024-04-04T10%3A00%3A00.123456Z&before_path=Factures%2Fgaz.pdf': {
        status: 200,
        body: { items: [edf], next: null },
      },
    })
    render(<DocumentsPage category="factures" />)
    fireEvent.click(await screen.findByRole('button', { name: 'Afficher plus' }))
    expect(await screen.findByText('edf.pdf')).toBeInTheDocument()
    expect(screen.getByText('gaz.pdf')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Afficher plus' })).not.toBeInTheDocument()
  })

  it('opens the folder of a document the viewer cannot show', async () => {
    mockApi({
      'GET /api/documents/categories': { status: 200, body: summary({ contrats: 1 }) },
      [page('contrats')]: { status: 200, body: { items: [bail], next: null } },
    })
    render(<DocumentsPage category="contrats" />)
    fireEvent.click(await screen.findByRole('link', { name: 'bail.docx' }))
    expect(window.location.pathname).toBe('/drive/Logement')
  })
})
