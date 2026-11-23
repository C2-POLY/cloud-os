import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { BoxStatus } from '../../../shared/api-types.ts'
import type { Account } from '../api.ts'
import { navigate } from '../router.ts'
import { mockApi } from '../test-api.ts'
import { appOf } from './apps.ts'
import { Desktop } from './Desktop.tsx'
import { clampPosition, defaultGeometry } from './windows.ts'

const enzo: Account = { name: 'enzo', display_name: 'Enzo', role: 'admin', created_at: '2026-11-17T10:00:00Z' }
const johan: Account = { ...enzo, name: 'johan', display_name: 'Johan', role: 'member' }

/** The box as an admin sees it: SMB down and the last backup failed. */
const boxStatus: BoxStatus = {
  version: '0.1.0',
  checked_at: '2026-11-23T10:00:00Z',
  services: [
    { name: 'core', state: 'ok' },
    { name: 'worker', state: 'ok', seen_at: '2026-11-23T09:59:58Z', analysis: 'ok', search: 'ok' },
    { name: 'smb', state: 'down' },
    { name: 'tailscale', state: 'off' },
  ],
  access: { public_url: '', funnel: false },
  backup: { on: true, last: { backup: { started_at: '2026-11-23T03:00:00Z', finished_at: '2026-11-23T03:00:01Z', ok: false, state: 'failed', error: 'no restic repository' } } },
  smb_kept: { waiting: 0, in_trash: 0 },
  alerts: [{ kind: 'service_down', service: 'smb' }, { kind: 'backup_failed' }],
}

function api() {
  return mockApi({
    'GET /api/health': { status: 200, body: { version: '0.1.0', db: 'ok', migration: 10, tasks: { queued: 2, running: 1, failed: 0 } } },
    'GET /api/box/status': { status: 200, body: boxStatus },
    'GET /api/analysis': { status: 200, body: { analysing: 0, failed: 0 } },
    'GET /api/files?path=': { status: 200, body: [] },
    'GET /api/files?path=Photos': { status: 200, body: [] },
    'GET /api/trash': { status: 200, body: [] },
    'GET /api/shares': { status: 200, body: [] },
    'GET /api/accounts': { status: 200, body: [enzo] },
  })
}

const windowNamed = (name: string) => screen.queryByRole('region', { name })

beforeEach(() => {
  window.localStorage.clear()
  window.history.replaceState(null, '', '/')
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('appOf', () => {
  it('maps addresses to apps, and the rest to the desktop', () => {
    expect(appOf('/drive')).toBe('files')
    expect(appOf('/drive/Photos/%C3%89t%C3%A9')).toBe('files')
    expect(appOf('/partages')).toBe('shares')
    expect(appOf('/corbeille')).toBe('trash')
    expect(appOf('/comptes')).toBe('accounts')
    expect(appOf('/etat')).toBe('status')
    expect(appOf('/')).toBeNull()
    expect(appOf('/nimporte')).toBeNull()
  })
})

describe('defaultGeometry', () => {
  it('keeps every new window above the dock, however far down the cascade', () => {
    for (let i = 0; i < 8; i++) {
      const g = defaultGeometry(i)
      expect(g.y + g.h).toBeLessThanOrEqual(window.innerHeight - 110)
    }
  })
})

describe('clampPosition', () => {
  it('keeps the title bar below the menu bar and on the screen', () => {
    expect(clampPosition(-5000, -50, 800).y).toBe(40)
    expect(clampPosition(-5000, 100, 800).x).toBe(80 - 800)
    expect(clampPosition(99999, 100, 800).x).toBe(window.innerWidth - 80)
  })
})

describe('Desktop', () => {
  it('opens apps in windows from the dock, with the address of the front one', async () => {
    api()
    render(<Desktop me={enzo} onSignedOut={() => {}} />)
    expect(screen.getByRole('main', { name: 'Bureau' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Fichiers' }))
    expect(window.location.pathname).toBe('/drive')
    expect(windowNamed('Fichiers')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Corbeille' }))
    expect(window.location.pathname).toBe('/corbeille')
    // Both stay open; Fichiers keeps its address and comes back forward.
    expect(windowNamed('Fichiers')).toBeInTheDocument()
    expect(windowNamed('Corbeille')).toHaveClass('front')
    fireEvent.click(screen.getByRole('button', { name: 'Fichiers' }))
    expect(window.location.pathname).toBe('/drive')
    expect(windowNamed('Fichiers')).toHaveClass('front')
  })

  it('keeps a window at the folder it shows while another is in front', () => {
    api()
    window.history.replaceState(null, '', '/drive/Photos')
    render(<Desktop me={enzo} onSignedOut={() => {}} />)
    act(() => navigate('/partages'))
    fireEvent.click(screen.getByRole('button', { name: 'Fichiers' }))
    expect(window.location.pathname).toBe('/drive/Photos')
  })

  it('closes the front window and shows the next one, then the desktop', () => {
    api()
    render(<Desktop me={enzo} onSignedOut={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Fichiers' }))
    fireEvent.click(screen.getByRole('button', { name: 'Corbeille' }))
    fireEvent.click(screen.getByRole('button', { name: 'Fermer Corbeille' }))
    return vi.waitFor(() => {
      expect(windowNamed('Corbeille')).not.toBeInTheDocument()
      expect(window.location.pathname).toBe('/drive')
    }).then(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'Fermer Fichiers' }))
      await vi.waitFor(() => expect(window.location.pathname).toBe('/'))
    })
  })

  it('sends unknown addresses and other people’s apps to the desktop', () => {
    api()
    window.history.replaceState(null, '', '/comptes')
    render(<Desktop me={johan} onSignedOut={() => {}} />)
    expect(window.location.pathname).toBe('/')
    expect(windowNamed('Comptes')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Comptes' })).not.toBeInTheDocument()
    act(() => navigate('/nimporte'))
    expect(window.location.pathname).toBe('/')
  })

  it('shows the admin a badge of alerts that opens État de la box, instead of a backup status it did not know', async () => {
    api()
    render(<Desktop me={enzo} onSignedOut={() => {}} />)
    const bar = screen.getByRole('banner')
    const badge = await within(bar).findByRole('button', { name: '2 alertes : voir l’état de la box' })
    expect(within(bar).queryByText('Sauvegarde à jour')).not.toBeInTheDocument()

    fireEvent.click(badge)
    expect(window.location.pathname).toBe('/etat')
    const win = windowNamed('État de la box')!
    expect(win).toHaveClass('front')
    expect(await within(win).findByText('Le partage SMB ne répond plus.')).toBeInTheDocument()
    expect(within(win).getByText('La dernière sauvegarde a échoué', { exact: false })).toBeInTheDocument()
    // The core's health moved here; Comptes keeps the accounts only.
    expect(await within(win).findByText('joignable')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Comptes' }))
    const accounts = windowNamed('Comptes')!
    expect(await within(accounts).findByText('Enzo')).toBeInTheDocument()
    expect(within(accounts).queryByText('joignable')).not.toBeInTheDocument()
  })

  it('shows no badge while all is well', async () => {
    const calls = mockApi({
      'GET /api/box/status': { status: 200, body: { ...boxStatus, alerts: [] } },
      'GET /api/analysis': { status: 200, body: { analysing: 0, failed: 0 } },
    })
    render(<Desktop me={enzo} onSignedOut={() => {}} />)
    await vi.waitFor(() => expect(calls.some((c) => c.key === 'GET /api/box/status')).toBe(true))
    expect(await screen.findByText('Analyse à jour')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /alerte/ })).not.toBeInTheDocument()
  })

  it('shows a member neither the app nor the badge, and never asks for the status', async () => {
    const calls = api()
    window.history.replaceState(null, '', '/etat')
    render(<Desktop me={johan} onSignedOut={() => {}} />)
    expect(window.location.pathname).toBe('/')
    expect(windowNamed('État de la box')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'État de la box' })).not.toBeInTheDocument()
    expect(await screen.findByText('Analyse à jour')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /alerte/ })).not.toBeInTheDocument()
    expect(screen.queryByText('Sauvegarde à jour')).not.toBeInTheDocument()
    expect(calls.map((c) => c.key)).not.toContain('GET /api/box/status')
  })

  it('grows a window and remembers it in this browser', () => {
    api()
    window.history.replaceState(null, '', '/corbeille')
    const { unmount } = render(<Desktop me={enzo} onSignedOut={() => {}} />)
    const win = windowNamed('Corbeille')!
    fireEvent.click(within(win).getByRole('button', { name: 'Agrandir Corbeille' }))
    expect(win).toHaveClass('max')
    unmount()
    render(<Desktop me={enzo} onSignedOut={() => {}} />)
    expect(windowNamed('Corbeille')).toHaveClass('max')
    fireEvent.doubleClick(windowNamed('Corbeille')!.querySelector('.window-title')!)
    expect(windowNamed('Corbeille')).not.toHaveClass('max')
  })

  it('switches Fichiers between its folders and its timeline', () => {
    api()
    window.history.replaceState(null, '', '/drive')
    render(<Desktop me={enzo} onSignedOut={() => {}} />)
    const views = screen.getByRole('navigation', { name: 'Vue de Fichiers' })
    fireEvent.click(within(views).getByRole('link', { name: 'Chronologie' }))
    expect(window.location.pathname).toBe('/chronologie')
    expect(windowNamed('Fichiers')).toHaveClass('front')
    fireEvent.click(within(screen.getByRole('navigation', { name: 'Vue de Fichiers' })).getByRole('link', { name: 'Dossiers' }))
    expect(window.location.pathname).toBe('/drive')
  })

  it('works without browser storage', () => {
    api()
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    window.history.replaceState(null, '', '/corbeille')
    render(<Desktop me={enzo} onSignedOut={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Agrandir Corbeille' }))
    expect(windowNamed('Corbeille')).toHaveClass('max')
    vi.restoreAllMocks()
  })

  it('opens Demander from the dock, the menu bar and ⌘K or Ctrl+K', () => {
    api()
    render(<Desktop me={enzo} onSignedOut={() => {}} />)
    const palette = () => screen.queryByRole('dialog', { name: 'Demander' })
    fireEvent.click(screen.getByRole('button', { name: 'Demander' }))
    expect(palette()).toBeInTheDocument()
    fireEvent.keyDown(screen.getByRole('combobox', { name: /Chercher/ }), { key: 'Escape' })
    expect(palette()).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /Demander ⌘K/ }))
    expect(palette()).toBeInTheDocument()
    fireEvent.keyDown(window, { key: 'k', metaKey: true })
    expect(palette()).not.toBeInTheDocument()
    fireEvent.keyDown(window, { key: 'K', ctrlKey: true })
    expect(palette()).toBeInTheDocument()
  })
})
