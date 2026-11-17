import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { App } from './App.tsx'
import { mockApi } from './test-api.ts'

const health = { status: 200, body: { version: '0.1.0', db: 'ok', migration: 3 } }
const enzo = { name: 'enzo', display_name: 'Enzo', role: 'admin', created_at: '2026-11-17T10:00:00Z' }
const johan = { name: 'johan', display_name: 'Johan', role: 'member', created_at: '2026-11-17T10:00:00Z' }

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } })
}

const emptyRoot = { status: 200, body: [] }

afterEach(() => {
  vi.unstubAllGlobals()
  window.history.replaceState(null, '', '/')
})

describe('App', () => {
  it('shows the first-start wizard, then the home page once the admin exists', async () => {
    const calls = mockApi({
      'GET /api/setup': { status: 200, body: { needed: true } },
      'POST /api/setup': { status: 201, body: { account: enzo, token: 't' } },
      'GET /api/health': health,
      'GET /api/accounts': { status: 200, body: [enzo] },
      'GET /api/files?path=': emptyRoot,
    })

    render(<App />)
    expect(await screen.findByText('Créer le compte administrateur')).toBeInTheDocument()
    fill('Identifiant', 'enzo')
    fill('Nom affiché', 'Enzo')
    fill('Mot de passe', 'correct horse battery')
    fireEvent.click(screen.getByText('Créer le compte administrateur'))

    // The drive opens, with the admin's Comptes tab.
    expect(await screen.findByText(/Ce dossier est vide/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /Comptes/ })).toBeInTheDocument()
    expect(screen.getByText('Enzo')).toBeInTheDocument()
    expect(window.location.pathname).toBe('/drive')
    expect(calls.find((c) => c.key === 'POST /api/setup')?.body).toEqual({
      name: 'enzo',
      display_name: 'Enzo',
      password: 'correct horse battery',
    })
  })

  it('asks to sign in when there is no session, and shows the error of a failed login', async () => {
    mockApi({
      'GET /api/setup': { status: 200, body: { needed: false } },
      'GET /api/me': { status: 401, body: { error: 'not signed in' } },
      'POST /api/login': [
        { status: 401, body: { error: 'wrong name or password' } },
        { status: 200, body: { account: johan, token: 't' } },
      ],
      'GET /api/health': health,
      'GET /api/files?path=': emptyRoot,
    })

    render(<App />)
    expect(await screen.findByText('Connexion')).toBeInTheDocument()
    fill('Identifiant', 'johan')
    fill('Mot de passe', 'nope')
    fireEvent.click(screen.getByText('Se connecter'))
    expect(await screen.findByRole('alert')).toHaveTextContent('Identifiant ou mot de passe incorrect.')

    fireEvent.click(screen.getByText('Se connecter'))
    expect(await screen.findByLabelText('Se déconnecter')).toBeInTheDocument()
    // A member does not see the account management.
    expect(screen.queryByRole('link', { name: /Comptes/ })).not.toBeInTheDocument()
  })

  it('lets the admin disable another account but not act on their own', async () => {
    const calls = mockApi({
      'GET /api/setup': { status: 200, body: { needed: false } },
      'GET /api/me': { status: 200, body: enzo },
      'GET /api/health': health,
      'GET /api/accounts': { status: 200, body: [enzo, johan] },
      'PATCH /api/accounts/johan': { status: 200, body: { ...johan, disabled_at: '2026-11-17T11:00:00Z' } },
    })
    window.history.replaceState(null, '', '/comptes')

    render(<App />)
    expect(await screen.findByText('Johan')).toBeInTheDocument()
    expect(screen.getAllByText('Désactiver')).toHaveLength(1)
    fireEvent.click(screen.getByText('Désactiver'))
    await vi.waitFor(() => expect(calls.some((c) => c.key === 'PATCH /api/accounts/johan')).toBe(true))
    expect(calls.find((c) => c.key === 'PATCH /api/accounts/johan')?.body).toEqual({ disabled: true })
  })
})
