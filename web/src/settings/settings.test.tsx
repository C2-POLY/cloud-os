import { fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Account } from '../api.ts'
import { appOf } from '../desktop/apps.ts'
import { Desktop } from '../desktop/Desktop.tsx'
import { mockApi } from '../test-api.ts'
import { SMBAccess } from './Settings.tsx'

const johan: Account = { name: 'johan', display_name: 'Johan', role: 'member', created_at: '2026-11-17T10:00:00Z' }
const none = { exists: false, created_at: null, last_used_at: null }
const made = { exists: true, created_at: '2026-11-19T10:00:00Z', last_used_at: null }
const used = { ...made, last_used_at: '2026-11-19T11:30:00Z' }
const first = 'Ab3dEfGh7kMnPqRsTuVwXyZ2'
const second = 'Zz9yXwVuTsRqPnMkJhGfEdCb'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('SMBAccess', () => {
  it('draws a password, shows it once with a Copier button, then only its dates', async () => {
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    const calls = mockApi({
      'GET /api/me/smb-password': { status: 200, body: none },
      'POST /api/me/smb-password': { status: 201, body: { ...made, password: first } },
    })
    render(<SMBAccess me={johan} />)

    expect(await screen.findByText('Aucun')).toBeInTheDocument()
    expect(screen.getByText('johan')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Révoquer' })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'Générer un mot de passe' }))
    const field = await screen.findByLabelText('Mot de passe SMB')
    expect(field).toHaveValue(first)
    expect(screen.getByText(/il ne sera plus affiché/)).toBeInTheDocument()
    expect(calls.find((c) => c.key === 'POST /api/me/smb-password')?.body).toEqual({})
    expect(screen.getByText('Défini')).toBeInTheDocument()
    expect(screen.getByText('Jamais')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Copier' }))
    expect(await screen.findByRole('button', { name: 'Copié' })).toBeInTheDocument()
    expect(writeText).toHaveBeenCalledWith(first)

    // Once hidden, the password is gone from the page.
    fireEvent.click(screen.getByRole('button', { name: /C’est noté/ }))
    expect(screen.queryByLabelText('Mot de passe SMB')).toBeNull()
    expect(document.body.textContent).not.toContain(first)
  })

  it('shows when the password was drawn and last used, never the password', async () => {
    mockApi({ 'GET /api/me/smb-password': { status: 200, body: used } })
    render(<SMBAccess me={johan} />)
    expect(await screen.findByText('Dernière utilisation')).toBeInTheDocument()
    expect(screen.queryByText('Jamais')).toBeNull()
    expect(screen.getByText('Créé le')).toBeInTheDocument()
    expect(screen.queryByLabelText('Mot de passe SMB')).toBeNull()
  })

  it('regenerates and revokes only after a confirmation', async () => {
    const calls = mockApi({
      'GET /api/me/smb-password': { status: 200, body: used },
      'POST /api/me/smb-password': { status: 201, body: { ...made, password: second } },
      'DELETE /api/me/smb-password': { status: 204 },
    })
    render(<SMBAccess me={johan} />)

    fireEvent.click(await screen.findByRole('button', { name: 'Régénérer' }))
    let dialog = await screen.findByRole('dialog', { name: 'Régénérer le mot de passe SMB ?' })
    expect(within(dialog).getByText(/connexions SMB ouvertes/)).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Annuler' }))
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(calls.some((c) => c.key === 'POST /api/me/smb-password')).toBe(false)

    fireEvent.click(screen.getByRole('button', { name: 'Régénérer' }))
    dialog = await screen.findByRole('dialog', { name: 'Régénérer le mot de passe SMB ?' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Régénérer' }))
    expect(await screen.findByLabelText('Mot de passe SMB')).toHaveValue(second)
    expect(screen.getByText('Jamais')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Révoquer' }))
    dialog = await screen.findByRole('dialog', { name: 'Révoquer le mot de passe SMB ?' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Révoquer' }))
    expect(await screen.findByText('Aucun')).toBeInTheDocument()
    expect(calls.some((c) => c.key === 'DELETE /api/me/smb-password')).toBe(true)
    expect(screen.queryByLabelText('Mot de passe SMB')).toBeNull()
    expect(screen.getByRole('button', { name: 'Générer un mot de passe' })).toBeInTheDocument()
  })

  it('says why it failed', async () => {
    mockApi({
      'GET /api/me/smb-password': { status: 200, body: none },
      'POST /api/me/smb-password': { status: 401, body: { error: 'not signed in' } },
    })
    render(<SMBAccess me={johan} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Générer un mot de passe' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Session expirée')
  })
})

describe('Réglages', () => {
  it('is in the dock of every member, at /reglages', async () => {
    mockApi({
      'GET /api/health': { status: 200, body: { version: '0.3.0', db: 'ok' } },
      'GET /api/analysis': { status: 200, body: { analysing: 0, failed: 0 } },
      'GET /api/me/smb-password': { status: 200, body: none },
    })
    window.history.replaceState(null, '', '/')
    render(<Desktop me={johan} onSignedOut={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Réglages' }))
    expect(window.location.pathname).toBe('/reglages')
    expect(appOf('/reglages')).toBe('settings')
    const win = screen.getByRole('region', { name: 'Réglages' })
    expect(await within(win).findByRole('heading', { name: 'Accès SMB' })).toBeInTheDocument()
  })
})
