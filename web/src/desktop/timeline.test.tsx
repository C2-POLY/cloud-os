import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mockApi } from '../test-api.ts'
import { appOf } from './apps.ts'
import { Timeline } from './Timeline.tsx'
import { byMonth, fillMonths, monthLabel, startOfMonthAfter, type TimelineEntry } from './timeline.ts'

const tz = encodeURIComponent(Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC')
const item = (path: string, at: string, mime: string, taken = false): TimelineEntry => ({
  path,
  name: path.split('/').pop()!,
  kind: 'file',
  size: 10,
  mime,
  sha256: 'ab'.repeat(32),
  modified_at: '2026-11-17T10:00:00Z',
  at,
  taken,
})

const plage = item('Photos/plage.jpg', '2024-07-14T18:03:22', 'image/jpeg', true)
const phare = item('Photos/phare.png', '2024-07-02T09:00:00', 'image/png')
const bail = item('Documents/bail.pdf', '2023-03-02T00:30:00', 'application/pdf')

beforeEach(() => window.history.replaceState(null, '', '/chronologie'))
afterEach(() => vi.unstubAllGlobals())

describe('timeline helpers', () => {
  it('belongs to Fichiers, names months and jumps to a month', () => {
    expect(appOf('/chronologie')).toBe('files')
    expect(monthLabel('2024-07')).toBe('Juillet 2024')
    expect(startOfMonthAfter('2024-07')).toEqual({ at: '2024-08-01T00:00:00', path: '' })
    expect(startOfMonthAfter('2024-12')).toEqual({ at: '2025-01-01T00:00:00', path: '' })
  })

  it('fills the empty months of the strip, across years', () => {
    expect(fillMonths([{ month: '2024-11', count: 2 }, { month: '2025-02', count: 1 }])).toEqual([
      { month: '2024-11', count: 2 },
      { month: '2024-12', count: 0 },
      { month: '2025-01', count: 0 },
      { month: '2025-02', count: 1 },
    ])
    expect(fillMonths([])).toEqual([])
  })

  it('groups by month in order, photos apart from documents', () => {
    const g = byMonth([plage, phare, bail])
    expect(g.map((m) => [m.month, m.media.length, m.docs.length])).toEqual([
      ['2024-07', 2, 0],
      ['2023-03', 0, 1],
    ])
  })
})

describe('Timeline', () => {
  it('shows the months, opens a file’s folder, and loads older pages', async () => {
    const calls = mockApi({
      [`GET /api/timeline/months?tz=${tz}`]: { status: 200, body: [{ month: '2023-03', count: 1 }, { month: '2024-07', count: 2 }] },
      [`GET /api/timeline?tz=${tz}&limit=60`]: { status: 200, body: { items: [plage, phare], next: { at: phare.at, path: phare.path } } },
      [`GET /api/timeline?tz=${tz}&limit=60&before_at=2024-07-02T09%3A00%3A00&before_path=Photos%2Fphare.png`]: { status: 200, body: { items: [bail], next: null } },
    })
    render(<Timeline />)
    expect(await screen.findByRole('heading', { name: 'Juillet 2024' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Juillet 2024, 2 fichiers' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Plus ancien' }))
    expect(await screen.findByRole('heading', { name: 'Mars 2023' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Plus ancien' })).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /plage\.jpg/ }))
    expect(window.location.pathname).toBe('/drive/Photos')
    expect(calls.filter((c) => c.key.includes('/api/timeline?'))).toHaveLength(2)
  })

  it('jumps to a month from the strip', async () => {
    const calls = mockApi({
      [`GET /api/timeline/months?tz=${tz}`]: { status: 200, body: [{ month: '2023-03', count: 1 }, { month: '2024-07', count: 2 }] },
      [`GET /api/timeline?tz=${tz}&limit=60`]: { status: 200, body: { items: [plage, phare], next: null } },
      [`GET /api/timeline?tz=${tz}&limit=60&before_at=2023-04-01T00%3A00%3A00&before_path=`]: { status: 200, body: { items: [bail], next: null } },
    })
    render(<Timeline />)
    fireEvent.click(await screen.findByRole('button', { name: 'Mars 2023, 1 fichier' }))
    expect(await screen.findByRole('heading', { name: 'Mars 2023' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Juillet 2024' })).not.toBeInTheDocument()
    expect(calls.some((c) => c.key.includes('before_at=2023-04-01'))).toBe(true)
  })

  it('says when there is nothing yet', async () => {
    mockApi({
      [`GET /api/timeline/months?tz=${tz}`]: { status: 200, body: [] },
      [`GET /api/timeline?tz=${tz}&limit=60`]: { status: 200, body: { items: [], next: null } },
    })
    render(<Timeline />)
    expect(await screen.findByText("Aucun fichier pour l'instant.")).toBeInTheDocument()
  })
})
