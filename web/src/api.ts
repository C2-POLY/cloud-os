/** Body of GET /api/health, as the core's `httpapi.Health` writes it. */
export interface Health {
  version: string
  db: 'ok' | 'down'
  migration?: number
  workers?: { id: string; seen_at: string }[]
}

/** Reads the core's health; a 503 still carries a body that says the database is down. */
export async function fetchHealth(signal?: AbortSignal): Promise<Health> {
  const res = await fetch('/api/health', { signal })
  if (!res.ok && res.status !== 503) {
    throw new Error(`HTTP ${res.status}`)
  }
  return (await res.json()) as Health
}
