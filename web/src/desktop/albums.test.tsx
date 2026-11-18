import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mockApi } from '../test-api.ts'
import { AlbumPage, AlbumsPage } from './Albums.tsx'
import { albumFromURL, albumURL, type Album } from './albums.ts'
import { appOf } from './apps.ts'
import { Timeline } from './Timeline.tsx'
import type { TimelineEntry } from './timeline.ts'

const tz = encodeURIComponent(Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC')
const photo = (path: string, at: string): TimelineEntry => ({
  path,
  name: path.split('/').pop()!,
  kind: 'file',
  size: 2048,
  mime: 'image/jpeg',
  sha256: 'ab'.repeat(32),
  modified_at: '2026-11-17T10:00:00Z',
  at,
  taken: true,
})
const plage = photo('Photos/plage.jpg', '2024-07-14T18:03:22')
const phare = photo('Photos/phare.jpg', '2024-07-14T09:00:00')
const bretagne: Album = { id: 7, name: 'Bretagne', created_at: '2026-11-18T09:00:00Z', count: 2, cover: plage }

beforeEach(() => window.history.replaceState(null, '', '/photos/albums'))
afterEach(() => vi.unstubAllGlobals())

describe('album addresses', () => {
  it('belong to Photos', () => {
    expect(appOf('/photos/albums')).toBe('photos')
    expect(appOf(albumURL(7))).toBe('photos')
    expect(albumFromURL('/photos/albums/7')).toBe(7)
    expect(albumFromURL('/photos/albums')).toBeNull()
    expect(appOf('/photos/autre')).toBeNull()
  })
})

describe('Albums', () => {
  it('lists the albums with their cover and count, and opens one', async () => {
    mockApi({ 'GET /api/albums': { status: 200, body: [bretagne, { ...bretagne, id: 8, name: 'Vide', count: 0, cover: null }] } })
    render(<AlbumsPage />)
    const link = await screen.findByRole('link', { name: /Bretagne/ })
    expect(within(link).getByText('2 éléments')).toBeInTheDocument()
    expect(link.querySelector('img')).toHaveAttribute('src', '/api/files/thumb?path=Photos%2Fplage.jpg&v=abababababababab')
    expect(screen.getByRole('link', { name: /Vide/ })).toBeInTheDocument()
    fireEvent.click(link)
    expect(window.location.pathname).toBe('/photos/albums/7')
  })

  it('creates an album and goes to it', async () => {
    const calls = mockApi({
      'GET /api/albums': { status: 200, body: [] },
      'POST /api/albums': { status: 201, body: { ...bretagne, id: 9, count: 0, cover: null } },
    })
    render(<AlbumsPage />)
    fireEvent.click(await screen.findByRole('button', { name: /Nouvel album/ }))
    fireEvent.change(screen.getByLabelText('Nom'), { target: { value: 'Bretagne' } })
    fireEvent.click(screen.getByRole('button', { name: 'Créer' }))
    await waitFor(() => expect(window.location.pathname).toBe('/photos/albums/9'))
    expect(calls.find((c) => c.key === 'POST /api/albums')?.body).toEqual({ name: 'Bretagne' })
  })

  it('shows an album and takes a photo out of it, never deleting it', async () => {
    const calls = mockApi({
      'GET /api/albums/7': [
        { status: 200, body: bretagne },
        { status: 200, body: { ...bretagne, count: 1 } },
      ],
      [`GET /api/albums/7/items?tz=${tz}&limit=60`]: [
        { status: 200, body: { items: [plage, phare], next: null } },
        { status: 200, body: { items: [plage], next: null } },
      ],
      'POST /api/albums/7/items/remove': { status: 200, body: { ...bretagne, count: 1 } },
    })
    render(<AlbumPage id={7} />)
    expect(await screen.findByRole('heading', { name: 'Bretagne' })).toBeInTheDocument()
    await screen.findByRole('button', { name: /phare\.jpg/ })
    fireEvent.click(screen.getByRole('button', { name: 'Sélectionner' }))
    fireEvent.click(screen.getByRole('button', { name: /phare\.jpg/ }))
    fireEvent.click(screen.getByRole('button', { name: "Retirer de l'album" }))
    await waitFor(() => expect(screen.queryByRole('button', { name: /phare\.jpg/ })).not.toBeInTheDocument())
    expect(calls.find((c) => c.key === 'POST /api/albums/7/items/remove')?.body).toEqual({ paths: ['Photos/phare.jpg'] })
    expect(screen.getByText("1 retirée de l'album. Elle reste dans Photos.")).toBeInTheDocument()
    expect(await screen.findByText('1 élément')).toBeInTheDocument() // the count follows
    expect(calls.some((c) => c.key.startsWith('POST /api/files/trash') || c.key.startsWith('DELETE /api/files'))).toBe(false)
  })
})

describe('adding from Photos', () => {
  it('adds the selected photos to an album', async () => {
    window.history.replaceState(null, '', '/photos')
    const calls = mockApi({
      [`GET /api/timeline/months?tz=${tz}&filter=media`]: { status: 200, body: [{ month: '2024-07', count: 2 }] },
      [`GET /api/timeline?tz=${tz}&limit=60&filter=media`]: { status: 200, body: { items: [plage, phare], next: null } },
      'GET /api/albums': { status: 200, body: [bretagne] },
      'POST /api/albums/7/items': { status: 200, body: bretagne },
    })
    render(<Timeline photos />)
    await screen.findByRole('button', { name: /plage\.jpg/ })
    fireEvent.click(screen.getByRole('button', { name: 'Sélectionner' }))
    fireEvent.click(screen.getByRole('button', { name: /plage\.jpg/ }))
    fireEvent.click(screen.getByRole('button', { name: /phare\.jpg/ }))
    expect(screen.getByText('2 sélectionnées')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument() // selecting does not open the viewer
    fireEvent.click(screen.getByRole('button', { name: 'Ajouter à un album…' }))
    fireEvent.click(await screen.findByRole('button', { name: /Bretagne/ }))
    expect(await screen.findByText('2 ajoutées à « Bretagne ».')).toBeInTheDocument()
    expect(calls.find((c) => c.key === 'POST /api/albums/7/items')?.body).toEqual({ paths: ['Photos/plage.jpg', 'Photos/phare.jpg'] })
  })
})
