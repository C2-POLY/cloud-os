import { fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Entry, FoundBy, SearchHit } from '../drive/files.ts'
import { mockApi } from '../test-api.ts'
import { Palette } from './Palette.tsx'
import { filtersOf, folderOf, foundByText } from './search.ts'

const at = '2026-11-17T10:00:00Z'
const dir = (path: string): Entry => ({ path, name: path.split('/').pop()!, kind: 'dir', size: 0, mime: 'inode/directory', modified_at: at })
const file = (path: string, mime: string): Entry => ({ path, name: path.split('/').pop()!, kind: 'file', size: 2048, mime, sha256: 'ab'.repeat(32), modified_at: at })
const hit = (e: Entry, found_by: FoundBy[], more: Partial<SearchHit> = {}): SearchHit => ({ ...e, score: 0.9, found_by, ...more })
const answer = (results: SearchHit[], partial = false) => ({ status: 200, body: { results, partial } })

const plage = file('Photos/Été/plage.jpg', 'image/jpeg')
const quittance = file('Documents/Quittance été.pdf', 'application/pdf')
const ete = dir('Photos/Été')
const edf = file('Factures/EDF mars.pdf', 'application/pdf')
const all = (q: string, extra = '') => `GET /api/search/all?q=${q}&limit=30${extra}`

beforeEach(() => window.history.replaceState(null, '', '/'))
/** The search field of Demander (the period is a combobox too). */
const box = () => screen.getByRole('combobox', { name: /Chercher/ })
const list = () => screen.getByRole('listbox', { name: 'Résultats' })

afterEach(() => vi.unstubAllGlobals())

describe('search helpers', () => {
  it('finds the folder that shows an entry', () => {
    expect(folderOf(ete)).toBe('Photos/Été')
    expect(folderOf(plage)).toBe('Photos/Été')
    expect(folderOf(file('notes.txt', 'text/plain'))).toBe('')
  })

  it('turns a type and a period into the filters of the core, counted from today', () => {
    const today = new Date(2026, 10, 19, 15, 30) // 19 November 2026, on the browser's clock
    expect(filtersOf('', '', today)).toEqual({})
    expect(filtersOf('photos', '', today)).toEqual({ type: 'photos' })
    expect(filtersOf('', 'month', today)).toEqual({ from: '2026-10-21', to: '2026-11-19' })
    expect(filtersOf('docs', 'year', today)).toEqual({ type: 'docs', from: '2026-01-01' })
    expect(filtersOf('', 'last-year', today)).toEqual({ from: '2025-01-01', to: '2025-12-31' })
    expect(filtersOf('', 'older', today)).toEqual({ to: '2024-12-31' })
    expect(filtersOf('', 'month', new Date(2026, 2, 5))).toEqual({ from: '2026-02-04', to: '2026-03-05' })
  })

  it('says how a file was found', () => {
    expect(foundByText(['name', 'text', 'meaning', 'image'])).toBe("le nom, les mots du texte, le sens du texte, ce que montre l'image")
  })
})

describe('Palette', () => {
  function type(text: string) {
    fireEvent.change(box(), { target: { value: text } })
  }

  it('shows the core’s one list while typing, with a preview of the selected one', async () => {
    const calls = mockApi({ [all('ete')]: answer([hit(quittance, ['name', 'meaning']), hit(plage, ['image']), hit(ete, ['name'])]) })
    render(<Palette onClose={() => {}} />)
    expect(screen.getByText(/Tape un nom/)).toBeInTheDocument()
    type('ete')
    const options = await within(list()).findAllByRole('option')
    // In the core's order, documents and photos together, no groups.
    expect(options.map((o) => o.textContent)).toEqual(['Quittance été.pdfDocuments', 'plage.jpgPhotos/Été', 'ÉtéPhotos'])
    expect(options[0]).toHaveAttribute('aria-selected', 'true')
    const first = screen.getByRole('complementary', { name: 'Aperçu de Quittance été.pdf' })
    expect(within(first).getByText('le nom, le sens du texte')).toBeInTheDocument()

    fireEvent.keyDown(box(), { key: 'ArrowDown' })
    const preview = screen.getByRole('complementary', { name: 'Aperçu de plage.jpg' })
    expect(within(preview).getByText('2 Ko')).toBeInTheDocument()
    expect(within(preview).getByText("ce que montre l'image")).toBeInTheDocument()
    expect(within(preview).getByRole('link', { name: /Télécharger/ })).toHaveAttribute('href', '/api/files/content?path=Photos%2F%C3%89t%C3%A9%2Fplage.jpg')
    // A photo has its thumbnail in its line.
    expect(options[1].querySelector('img.palette-thumb')).toHaveAttribute('src', expect.stringContaining('/api/files/thumb?path=Photos%2F'))
    expect(calls.map((c) => c.key)).toEqual([all('ete')])
  })

  it('opens the selected entry in Fichiers with Enter, and wraps around with the arrows', async () => {
    mockApi({ [all('ete')]: answer([hit(plage, ['name']), hit(quittance, ['name'])]) })
    const onClose = vi.fn()
    render(<Palette onClose={onClose} />)
    type('ete')
    await within(list()).findAllByRole('option')
    fireEvent.keyDown(box(), { key: 'ArrowUp' })
    fireEvent.keyDown(box(), { key: 'Enter' })
    expect(window.location.pathname).toBe('/drive/Documents')
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('says when nothing is found, and shows the error of a failed search', async () => {
    mockApi({ [all('zzz')]: answer([]), [all('boom')]: { status: 500, body: { error: 'database down' } } })
    render(<Palette onClose={() => {}} />)
    type('zzz')
    expect(await screen.findByText('Rien trouvé pour « zzz ».')).toBeInTheDocument()
    type('boom')
    expect(await screen.findByText(/database down|erreur|500/i)).toBeInTheDocument()
  })

  it('asks only once the typing pauses, and closes with Escape or a click beside it', async () => {
    const calls = mockApi({ [all('plage')]: answer([hit(plage, ['name'])]) })
    const onClose = vi.fn()
    const { container } = render(<Palette onClose={onClose} />)
    for (const text of ['p', 'pl', 'pla', 'plag', 'plage']) type(text)
    await within(list()).findAllByRole('option')
    expect(calls.map((c) => c.key)).toEqual([all('plage')])
    fireEvent.keyDown(box(), { key: 'Escape' })
    fireEvent.mouseDown(container.querySelector('.palette-backdrop')!)
    fireEvent.keyDown(screen.getByRole('button', { name: 'Photos' }), { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(3)
  })

  it('shows the words found in a file’s text, marked, and never reads them as HTML', async () => {
    const snippet = [{ text: 'Facture d’' }, { text: 'ÉLECTRICITÉ', hit: true }, { text: ' <b>mars</b>' }]
    mockApi({ [all('electricite')]: answer([hit(edf, ['text', 'meaning'], { snippet, excerpt: 'ignored' })]) })
    render(<Palette onClose={() => {}} />)
    type('electricite')
    const [option] = await within(list()).findAllByRole('option')
    expect(option).toHaveTextContent('EDF mars.pdfFacturesFacture d’ÉLECTRICITÉ <b>mars</b>')
    expect(within(option).getByText('ÉLECTRICITÉ').tagName).toBe('MARK')
    expect(option.querySelector('b')).toBeNull()
    const preview = screen.getByRole('complementary', { name: 'Aperçu de EDF mars.pdf' })
    expect(within(preview).getByLabelText('Extrait')).toHaveTextContent('Facture d’ÉLECTRICITÉ <b>mars</b>')
  })

  it('shows the passage nearest in meaning when no word was found', async () => {
    const calls = mockApi({ [all("contrat+de+l%27appart")]: answer([hit(quittance, ['meaning'], { excerpt: 'Contrat de location <i>meublée</i>' })]) })
    render(<Palette onClose={() => {}} />)
    type("contrat de l'appart")
    const [option] = await within(list()).findAllByRole('option')
    expect(option).toHaveTextContent('Contrat de location <i>meublée</i>')
    expect(option.querySelector('i')).toBeNull()
    expect(within(screen.getByRole('complementary')).getByLabelText('Extrait')).toHaveTextContent('Contrat de location')
    expect(calls.map((c) => c.key)).toEqual([all('contrat+de+l%27appart')])
  })

  it('narrows the list by type and by period, and asks again', async () => {
    const tz = encodeURIComponent(Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC')
    const year = new Date().getFullYear()
    const calls = mockApi({
      [all('chien')]: answer([hit(plage, ['image']), hit(edf, ['text'])]),
      [all('chien', '&type=photos')]: answer([hit(plage, ['image'])]),
      [all('chien', `&type=photos&from=${year - 1}-01-01&to=${year - 1}-12-31&tz=${tz}`)]: answer([]),
    })
    render(<Palette onClose={() => {}} />)
    type('chien')
    expect(await within(list()).findAllByRole('option')).toHaveLength(2)
    expect(screen.getByRole('button', { name: 'Tout' })).toHaveAttribute('aria-pressed', 'true')
    fireEvent.click(screen.getByRole('button', { name: 'Photos' }))
    expect(screen.getByRole('button', { name: 'Photos' })).toHaveAttribute('aria-pressed', 'true')
    expect(box()).toHaveFocus()
    await vi.waitFor(() => expect(within(list()).getAllByRole('option')).toHaveLength(1))
    fireEvent.change(screen.getByRole('combobox', { name: 'Période' }), { target: { value: 'last-year' } })
    expect(await screen.findByText('Rien trouvé pour « chien » avec ces filtres.')).toBeInTheDocument()
    expect(calls.map((c) => c.key)).toEqual([
      all('chien'),
      all('chien', '&type=photos'),
      all('chien', `&type=photos&from=${year - 1}-01-01&to=${year - 1}-12-31&tz=${tz}`),
    ])
  })

  it('says when the search by meaning is missing, and keeps the names and words', async () => {
    mockApi({ [all('bail')]: answer([hit(quittance, ['text'])], true) })
    render(<Palette onClose={() => {}} />)
    type('bail')
    expect(await screen.findByText(/La recherche par le sens ne répond pas/)).toBeInTheDocument()
    expect(within(list()).getAllByRole('option').map((o) => o.textContent)).toEqual(['Quittance été.pdfDocuments'])
  })
})
