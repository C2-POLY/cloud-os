import { fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Entry } from '../drive/files.ts'
import { mockApi } from '../test-api.ts'
import { Palette } from './Palette.tsx'
import { folderOf, groupOf } from './search.ts'

const at = '2026-11-17T10:00:00Z'
const dir = (path: string): Entry => ({ path, name: path.split('/').pop()!, kind: 'dir', size: 0, mime: 'inode/directory', modified_at: at })
const file = (path: string, mime: string): Entry => ({ path, name: path.split('/').pop()!, kind: 'file', size: 2048, mime, sha256: 'ab'.repeat(32), modified_at: at })

const plage = file('Photos/Été/plage.jpg', 'image/jpeg')
const quittance = file('Documents/Quittance été.pdf', 'application/pdf')
const ete = dir('Photos/Été')

beforeEach(() => window.history.replaceState(null, '', '/'))
afterEach(() => vi.unstubAllGlobals())

describe('search helpers', () => {
  it('groups entries and finds the folder that shows them', () => {
    expect([ete, plage, quittance].map(groupOf)).toEqual(['Dossiers', 'Photos et vidéos', 'Documents'])
    expect(groupOf(file('film.mp4', 'video/mp4'))).toBe('Photos et vidéos')
    expect(folderOf(ete)).toBe('Photos/Été')
    expect(folderOf(plage)).toBe('Photos/Été')
    expect(folderOf(file('notes.txt', 'text/plain'))).toBe('')
  })
})

describe('Palette', () => {
  function type(text: string) {
    fireEvent.change(screen.getByRole('combobox'), { target: { value: text } })
  }

  it('shows results grouped while typing, with a preview of the selected one', async () => {
    const calls = mockApi({ 'GET /api/search?q=ete&limit=30': { status: 200, body: [quittance, plage, ete] } })
    render(<Palette onClose={() => {}} />)
    expect(screen.getByText(/Tape un nom/)).toBeInTheDocument()
    type('ete')
    const options = await screen.findAllByRole('option')
    // Folders, then photos, then documents, whatever the core's order.
    expect(options.map((o) => o.textContent)).toEqual(['ÉtéPhotos', 'plage.jpgPhotos/Été', 'Quittance été.pdfDocuments'])
    expect(screen.getByText('Dossiers')).toBeInTheDocument()
    expect(options[0]).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('complementary', { name: 'Aperçu de Été' })).toBeInTheDocument()

    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'ArrowDown' })
    const preview = screen.getByRole('complementary', { name: 'Aperçu de plage.jpg' })
    expect(within(preview).getByText('2 Ko')).toBeInTheDocument()
    expect(within(preview).getByRole('link', { name: /Télécharger/ })).toHaveAttribute('href', '/api/files/content?path=Photos%2F%C3%89t%C3%A9%2Fplage.jpg')
    expect(calls.filter((c) => c.key.startsWith('GET /api/search'))).toHaveLength(1)
  })

  it('opens the selected entry in Fichiers with Enter, and wraps around with the arrows', async () => {
    mockApi({ 'GET /api/search?q=ete&limit=30': { status: 200, body: [plage, quittance] } })
    const onClose = vi.fn()
    render(<Palette onClose={onClose} />)
    type('ete')
    await screen.findAllByRole('option')
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'ArrowUp' })
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'Enter' })
    expect(window.location.pathname).toBe('/drive/Documents')
    expect(onClose).toHaveBeenCalledOnce()
  })

  it('says when nothing is found, and shows the error of a failed search', async () => {
    mockApi({
      'GET /api/search?q=zzz&limit=30': { status: 200, body: [] },
      'GET /api/search?q=boom&limit=30': { status: 500, body: { error: 'database down' } },
    })
    render(<Palette onClose={() => {}} />)
    type('zzz')
    expect(await screen.findByText('Rien trouvé pour « zzz ».')).toBeInTheDocument()
    type('boom')
    expect(await screen.findByText(/database down|erreur|500/i)).toBeInTheDocument()
  })

  it('asks only once the typing pauses, and closes with Escape or a click beside it', async () => {
    const calls = mockApi({ 'GET /api/search?q=plage&limit=30': { status: 200, body: [plage] } })
    const onClose = vi.fn()
    const { container } = render(<Palette onClose={onClose} />)
    for (const text of ['p', 'pl', 'pla', 'plag', 'plage']) type(text)
    await screen.findAllByRole('option')
    expect(calls.map((c) => c.key)).toEqual(['GET /api/search?q=plage&limit=30'])
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'Escape' })
    fireEvent.mouseDown(container.querySelector('.palette-backdrop')!)
    expect(onClose).toHaveBeenCalledTimes(2)
  })
})
