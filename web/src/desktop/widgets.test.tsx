import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mockApi } from '../test-api.ts'
import { dayLabel, describe as describeGroup, type ActivityGroup } from './widgets.ts'
import { Widgets } from './Widgets.tsx'

const g = (kind: ActivityGroup['kind'], place: string, count: number, name?: string): ActivityGroup => ({ day: '2026-11-17', kind, place, count, name, last: '2026-11-17T10:00:00Z' })
const tz = encodeURIComponent(Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC')

beforeEach(() => window.history.replaceState(null, '', '/'))
afterEach(() => vi.unstubAllGlobals())

describe('describe', () => {
  it('says what happened, and where a click goes', () => {
    expect(describeGroup(g('added', 'Photos', 46))).toEqual({ text: '46 fichiers ajoutés dans Photos', href: '/drive/Photos' })
    expect(describeGroup(g('added', '', 1, 'notes.txt'))).toEqual({ text: 'notes.txt ajouté dans Mon drive', href: '/drive' })
    expect(describeGroup(g('replaced', 'Docs', 1, 'Docs/budget.xlsx')).text).toBe("Nouvelle version de budget.xlsx, l'ancienne est gardée")
    expect(describeGroup(g('trashed', 'Photos/Été', 3))).toEqual({ text: '3 éléments mis à la corbeille depuis Photos/Été', href: '/corbeille' })
    expect(describeGroup(g('restored', 'Photos', 1, 'Photos/a.jpg')).text).toBe('a.jpg restauré dans Photos')
    expect(describeGroup(g('share_opened', 'Photos/Vacances', 3))).toEqual({ text: 'Lien « Vacances » ouvert 3 fois', href: '/partages' })
    expect(describeGroup(g('share_opened', 'Photos/Vacances', 1)).text).toBe('Lien « Vacances » ouvert')
  })
})

describe('dayLabel', () => {
  it('names today and yesterday, then the date', () => {
    const now = new Date(2026, 10, 17, 9, 0)
    expect(dayLabel('2026-11-17', now)).toBe("Aujourd'hui")
    expect(dayLabel('2026-11-16', now)).toBe('Hier')
    expect(dayLabel('2026-11-12', now)).toBe('Jeudi 12 novembre')
    expect(dayLabel('2026-12-31', new Date(2027, 0, 1))).toBe('Hier')
  })
})

describe('Widgets', () => {
  it('shows the recent files and the activity, and opens where they point', async () => {
    const today = new Date()
    const day = `${today.getFullYear()}-${String(today.getMonth() + 1).padStart(2, '0')}-${String(today.getDate()).padStart(2, '0')}`
    mockApi({
      'GET /api/files/recent?limit=8': { status: 200, body: [{ path: 'Photos/plage.jpg', name: 'plage.jpg', kind: 'file', size: 1, mime: 'image/jpeg', modified_at: '2026-11-17T10:00:00Z' }] },
      [`GET /api/activity?days=7&tz=${tz}`]: { status: 200, body: [{ ...g('added', 'Photos', 46), day }] },
    })
    render(<Widgets />)
    expect(await screen.findByText('46 fichiers ajoutés dans Photos')).toBeInTheDocument()
    expect(screen.getByText("Aujourd'hui")).toBeInTheDocument()
    fireEvent.click(screen.getByRole('link', { name: /plage\.jpg/ }))
    expect(window.location.pathname).toBe('/drive/Photos')
    fireEvent.click(screen.getByText('46 fichiers ajoutés dans Photos'))
    expect(window.location.pathname).toBe('/drive/Photos')
  })

  it('says when there is nothing yet, and survives a failing core', async () => {
    mockApi({
      'GET /api/files/recent?limit=8': { status: 200, body: [] },
      [`GET /api/activity?days=7&tz=${tz}`]: { status: 500, body: { error: 'down' } },
    })
    render(<Widgets />)
    expect(await screen.findByText(/apparaîtront ici/)).toBeInTheDocument()
    expect(screen.queryByText('Rien depuis une semaine.')).not.toBeInTheDocument()
  })

  it('reads again when the tab comes back into view', async () => {
    const calls = mockApi({
      'GET /api/files/recent?limit=8': { status: 200, body: [] },
      [`GET /api/activity?days=7&tz=${tz}`]: { status: 200, body: [] },
    })
    render(<Widgets />)
    await screen.findByText('Rien depuis une semaine.')
    document.dispatchEvent(new Event('visibilitychange'))
    await vi.waitFor(() => expect(calls.filter((c) => c.key.startsWith('GET /api/activity'))).toHaveLength(2))
  })
})
