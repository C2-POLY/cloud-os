import { fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mockApi } from '../test-api.ts'
import { appOf } from './apps.ts'
import { Timeline } from './Timeline.tsx'
import { byDay, dayTitle, formatWallClock, type TimelineEntry } from './timeline.ts'

const tz = encodeURIComponent(Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC')
const photo = (path: string, at: string, taken = true, mime = 'image/jpeg'): TimelineEntry => ({
  path,
  name: path.split('/').pop()!,
  kind: 'file',
  size: 2048,
  mime,
  sha256: 'ab'.repeat(32),
  modified_at: '2026-11-17T10:00:00Z',
  at,
  taken,
})
const a = photo('Photos/plage.jpg', '2024-07-14T18:03:22')
const b = photo('Photos/coucher.jpg', '2024-07-14T09:00:00')
const c = photo('Photos/phare.png', '2024-07-02T11:00:00', false, 'image/png')
const meta = { taken_at: '2024-07-14T18:03:22', taken_offset: '+02:00', latitude: 48.8584, longitude: 2.2945, make: 'Apple', model: 'iPhone 15', lens: null, width: 4032, height: 3024 }

beforeEach(() => window.history.replaceState(null, '', '/photos'))
afterEach(() => vi.unstubAllGlobals())

describe('photo helpers', () => {
  it('belongs to Photos, splits days and writes dates without time zone shifts', () => {
    expect(appOf('/photos')).toBe('photos')
    expect(byDay([a, b, c]).map((d) => [d.day, d.items.length])).toEqual([
      ['2024-07-14', 2],
      ['2024-07-02', 1],
    ])
    expect(dayTitle('2024-07-14')).toBe('Dimanche 14 juillet')
    expect(formatWallClock('2024-07-14T18:03:22')).toBe('Dimanche 14 juillet 2024 à 18:03')
  })
})

describe('Photos', () => {
  function api() {
    return mockApi({
      [`GET /api/timeline/months?tz=${tz}&filter=media`]: { status: 200, body: [{ month: '2024-07', count: 3 }] },
      [`GET /api/timeline?tz=${tz}&limit=60&filter=media`]: { status: 200, body: { items: [a, b, c], next: null } },
      'GET /api/files/meta?path=Photos%2Fplage.jpg': { status: 200, body: meta },
      'GET /api/files/meta?path=Photos%2Fcoucher.jpg': { status: 404, body: { error: 'no metadata' } },
    })
  }

  it('shows the photos by day and opens one big with its metadata', async () => {
    api()
    render(<Timeline photos />)
    expect(await screen.findByRole('heading', { name: 'Dimanche 14 juillet' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Mardi 2 juillet' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /plage\.jpg/ }))
    const viewer = screen.getByRole('dialog', { name: 'plage.jpg' })
    expect(within(viewer).getByRole('img', { name: 'plage.jpg' })).toHaveAttribute('src', '/api/files/content?path=Photos%2Fplage.jpg')
    expect(await within(viewer).findByText('Apple iPhone 15')).toBeInTheDocument()
    expect(within(viewer).getByText('4032 × 3024')).toBeInTheDocument()
    expect(within(viewer).getByText('48.85840, 2.29450')).toBeInTheDocument()
    expect(within(viewer).getByText('Prise le')).toBeInTheDocument()
    expect(within(viewer).getByText('1 / 3')).toBeInTheDocument()
  })

  it('moves with the arrows, shows what is known, and closes with Escape', async () => {
    api()
    render(<Timeline photos />)
    fireEvent.click(await screen.findByRole('button', { name: /plage\.jpg/ }))
    expect(screen.getByRole('button', { name: 'Plus récente' })).toBeDisabled()

    fireEvent.keyDown(window, { key: 'ArrowRight' })
    const viewer = screen.getByRole('dialog', { name: 'coucher.jpg' })
    expect(within(viewer).queryByText('Appareil')).not.toBeInTheDocument() // no metadata yet

    fireEvent.click(within(viewer).getByRole('button', { name: 'Plus ancienne' }))
    expect(screen.getByRole('dialog', { name: 'phare.png' })).toBeInTheDocument()
    expect(screen.getByText('Modifiée le')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Plus ancienne' })).toBeDisabled()

    fireEvent.keyDown(window, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('shows a photo in Fichiers from the viewer', async () => {
    api()
    render(<Timeline photos />)
    fireEvent.click(await screen.findByRole('button', { name: /plage\.jpg/ }))
    fireEvent.click(screen.getByRole('button', { name: /Afficher dans Fichiers/ }))
    expect(window.location.pathname).toBe('/drive/Photos')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})
