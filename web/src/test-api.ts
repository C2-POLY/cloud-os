import { vi } from 'vitest'

export type Reply = { status: number; body?: unknown }

/** Answers fetch from a table of "METHOD /path" → reply, and records the calls. */
export function mockApi(table: Record<string, Reply | Reply[]>) {
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

