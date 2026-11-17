import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Health } from './Health.tsx'

function mockFetch(status: number, body: unknown) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify(body), { status })),
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('Health', () => {
  it('shows the version, the database, the migration and the worker seen', async () => {
    mockFetch(200, {
      version: '0.1.0',
      db: 'ok',
      migration: 1,
      workers: [{ id: 'worker-a', seen_at: '2026-11-17T10:00:00Z' }],
    })

    render(<Health />)

    expect(await screen.findByText('v0.1.0')).toBeInTheDocument()
    expect(screen.getByText('joignable')).toBeInTheDocument()
    expect(screen.getByText('1')).toBeInTheDocument()
    expect(screen.getByText(/worker-a/)).toBeInTheDocument()
  })

  it('shows the worker as absent when no heartbeat is recent', async () => {
    mockFetch(200, { version: '0.1.0', db: 'ok', migration: 1 })

    render(<Health />)

    expect(await screen.findByText('absent')).toBeInTheDocument()
  })

  it('shows the database as down on a 503', async () => {
    mockFetch(503, { version: '0.1.0', db: 'down' })

    render(<Health />)

    expect(await screen.findByText('injoignable')).toBeInTheDocument()
    expect(screen.getByText('inconnu')).toBeInTheDocument()
  })

  it('shows the core as unreachable when the request fails', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new TypeError('Failed to fetch')
      }),
    )

    render(<Health />)

    expect(await screen.findByText('injoignable')).toBeInTheDocument()
    expect(screen.queryByText(/^v/)).not.toBeInTheDocument()
  })
})
