import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { App } from './App.tsx'

type Reply = { status: number; body?: unknown }

/** Answers fetch from a table of "METHOD /path" → reply, and records the calls. */
function mockApi(table: Record<string, Reply | Reply[]>) {
  const calls: { key: string; body?: unknown }[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path: string, init?: RequestInit) => {
      const key = `${init?.method ?? 'GET'} ${path}`
      calls.push({ key, body: init?.body ? JSON.parse(String(init.body)) : undefined })
      const entry = table[key]
      const reply = Array.isArray(entry) ? (entry.shift() ?? { status: 500 }) : (entry ?? { status: 404 })
      return new Response(reply.body === undefined ? null : JSON.stringify(reply.body), { status: reply.status })
    }),
  )
  return calls
}

const health = { status: 200, body: { version: '0.1.0', db: 'ok', migration: 3 } }
const enzo = { name: 'enzo', display_name: 'Enzo', role: 'admin', created_at: '2026-11-17T10:00:00Z' }
const johan = { name: 'johan', display_name: 'Johan', role: 'member', created_at: '2026-11-17T10:00:00Z' }

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } })
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('App', () => {
  it('shows the first-start wizard, then the home page once the admin exists', async () => {
    const calls = mockApi({
      'GET /api/setup': { status: 200, body: { needed: true } },
      'POST /api/setup': { status: 201, body: { account: enzo, token: 't' } },
      'GET /api/health': health,
      'GET /api/accounts': { status: 200, body: [enzo] },
    })

    render(<App />)
    expect(await screen.findByText('Créer le compte administrateur')).toBeInTheDocument()
    fill('Identifiant', 'enzo')
    fill('Nom affiché', 'Enzo')
    fill('Mot de passe', 'correct horse battery')
    fireEvent.click(screen.getByText('Créer le compte administrateur'))

    expect(await screen.findByText('Comptes')).toBeInTheDocument()
    expect(screen.getAllByText('Enzo').length).toBeGreaterThan(0)
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
    })

    render(<App />)
    expect(await screen.findByText('Connexion')).toBeInTheDocument()
    fill('Identifiant', 'johan')
    fill('Mot de passe', 'nope')
    fireEvent.click(screen.getByText('Se connecter'))
    expect(await screen.findByRole('alert')).toHaveTextContent('Identifiant ou mot de passe incorrect.')

    fireEvent.click(screen.getByText('Se connecter'))
    expect(await screen.findByText('Se déconnecter')).toBeInTheDocument()
    // A member does not see the account management.
    expect(screen.queryByText('Comptes')).not.toBeInTheDocument()
  })

  it('lets the admin disable another account but not act on their own', async () => {
    const calls = mockApi({
      'GET /api/setup': { status: 200, body: { needed: false } },
      'GET /api/me': { status: 200, body: enzo },
      'GET /api/health': health,
      'GET /api/accounts': { status: 200, body: [enzo, johan] },
      'PATCH /api/accounts/johan': { status: 200, body: { ...johan, disabled_at: '2026-11-17T11:00:00Z' } },
    })

    render(<App />)
    expect(await screen.findByText('Johan')).toBeInTheDocument()
    expect(screen.getAllByText('Désactiver')).toHaveLength(1)
    fireEvent.click(screen.getByText('Désactiver'))
    await vi.waitFor(() => expect(calls.some((c) => c.key === 'PATCH /api/accounts/johan')).toBe(true))
    expect(calls.find((c) => c.key === 'PATCH /api/accounts/johan')?.body).toEqual({ disabled: true })
  })
})
