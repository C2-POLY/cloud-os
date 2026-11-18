import type { Account, Album, Entry, MediaMeta, MonthCount, SignedIn, TimelineCursor, TimelinePage } from '../../../shared/api-types'

/** An error answered by the box, with its HTTP status. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message)
  }
}

/** Where the box is and the session token, once signed in. */
export interface Connection {
  box: string // "http://192.168.1.20:8480", no trailing slash
  token: string
}

/** "192.168.1.20:8480" → "http://192.168.1.20:8480"; keeps https; drops a trailing slash. */
export function normalizeBox(input: string): string {
  const trimmed = input.trim().replace(/\/+$/, '')
  if (!trimmed) return ''
  return /^https?:\/\//i.test(trimmed) ? trimmed : `http://${trimmed}`
}

/** Headers every request to the box carries: the session and the client marker the core asks for. */
export function authHeaders(token: string | null): Record<string, string> {
  const h: Record<string, string> = { 'X-Cloudos-Client': 'mobile' }
  if (token) h.Authorization = `Bearer ${token}`
  return h
}

async function call<T>(box: string, token: string | null, method: string, path: string, body?: unknown): Promise<T> {
  let res: Response
  try {
    res = await fetch(box + path, {
      method,
      headers: { ...authHeaders(token), ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch {
    throw new ApiError(0, 'La box ne répond pas à cette adresse.')
  }
  const text = await res.text()
  const json = text ? (JSON.parse(text) as unknown) : undefined
  if (!res.ok) {
    const message = (json as { error?: string } | undefined)?.error ?? `HTTP ${res.status}`
    throw new ApiError(res.status, message)
  }
  return json as T
}

/** POST /api/login: the account and the session token for this phone. */
export async function signIn(box: string, name: string, password: string): Promise<Connection & { account: Account }> {
  const res = await call<SignedIn & { token: string }>(box, null, 'POST', '/api/login', { name, password })
  return { box, token: res.token, account: res.account }
}

const q = encodeURIComponent

/** The calls the screens make, bound to one connection. */
export function client(c: Connection) {
  const get = <T>(path: string) => call<T>(c.box, c.token, 'GET', path)
  const zone = q(Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC')
  return {
    me: () => get<Account>('/api/me'),
    signOut: () => call<unknown>(c.box, c.token, 'POST', '/api/logout', {}),
    list: (folder: string) => get<Entry[]>(`/api/files?path=${q(folder)}`),
    timeline: (before: TimelineCursor | null) =>
      get<TimelinePage>(`/api/timeline?tz=${zone}&limit=90&filter=media${before ? `&before_at=${q(before.at)}&before_path=${q(before.path)}` : ''}`),
    known: async (sums: string[]) => (await call<{ known: string[] }>(c.box, c.token, 'POST', '/api/files/known', { sha256: sums })).known,
    albums: () => get<Album[]>('/api/albums'),
    albumItems: (id: number, before: TimelineCursor | null) =>
      get<TimelinePage>(`/api/albums/${id}/items?tz=${zone}&limit=90${before ? `&before_at=${q(before.at)}&before_path=${q(before.path)}` : ''}`),
    months: () => get<MonthCount[]>(`/api/timeline/months?tz=${zone}&filter=media`),
    meta: (path: string) => get<MediaMeta>(`/api/files/meta?path=${q(path)}`),
    contentURL: (path: string) => `${c.box}/api/files/content?path=${q(path)}`,
    thumbURL: (e: Entry) => (e.sha256 ? `${c.box}/api/files/thumb?path=${q(e.path)}&v=${e.sha256.slice(0, 16)}` : null),
    headers: authHeaders(c.token),
  }
}

export type Client = ReturnType<typeof client>
