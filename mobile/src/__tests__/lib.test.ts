import { ApiError, authHeaders, client, normalizeBox, signIn } from '../lib/api'
import { byMonth, formatSize, monthLabel, parentOf, sortEntries, wallClock } from '../lib/format'
import type { Entry, TimelineEntry } from '../../../shared/api-types'

const entry = (path: string, kind: 'file' | 'dir' = 'file'): Entry => ({ path, name: path.split('/').pop()!, kind, size: 1, mime: 'text/plain', modified_at: '2026-11-17T10:00:00Z' })

function mockFetch(status: number, body: unknown) {
  const calls: { url: string; init: RequestInit }[] = []
  globalThis.fetch = jest.fn(async (url: string, init: RequestInit) => {
    calls.push({ url, init })
    return new Response(body === undefined ? null : JSON.stringify(body), { status })
  }) as unknown as typeof fetch
  return calls
}

describe('api', () => {
  it('normalizes the box address', () => {
    expect(normalizeBox(' 192.168.1.20:8480/ ')).toBe('http://192.168.1.20:8480')
    expect(normalizeBox('https://box.maison')).toBe('https://box.maison')
    expect(normalizeBox('')).toBe('')
  })

  it('sends the session and the client marker the core asks for', () => {
    expect(authHeaders('t0k')).toEqual({ 'X-Cloudos-Client': 'mobile', Authorization: 'Bearer t0k' })
    expect(authHeaders(null)).toEqual({ 'X-Cloudos-Client': 'mobile' })
  })

  it('signs in and calls the box with the token', async () => {
    const calls = mockFetch(200, { account: { name: 'enzo' }, token: 'abc' })
    const c = await signIn('http://box', 'enzo', 'secret')
    expect(c).toMatchObject({ box: 'http://box', token: 'abc', account: { name: 'enzo' } })
    expect(calls[0].url).toBe('http://box/api/login')
    expect(JSON.parse(String(calls[0].init.body))).toEqual({ name: 'enzo', password: 'secret' })

    const listed = mockFetch(200, [])
    await client(c).list('Photos/Été')
    expect(listed[0].url).toBe('http://box/api/files?path=Photos%2F%C3%89t%C3%A9')
    expect((listed[0].init.headers as Record<string, string>).Authorization).toBe('Bearer abc')
    expect(client(c).contentURL('a b.jpg')).toBe('http://box/api/files/content?path=a%20b.jpg')
  })

  it('turns errors into ApiError with the box’s message', async () => {
    mockFetch(401, { error: 'wrong name or password' })
    await expect(signIn('http://box', 'enzo', 'x')).rejects.toMatchObject({ status: 401, message: 'wrong name or password' })
    globalThis.fetch = jest.fn(async () => {
      throw new TypeError('Network request failed')
    }) as unknown as typeof fetch
    await expect(signIn('http://nowhere', 'enzo', 'x')).rejects.toBeInstanceOf(ApiError)
  })
})

describe('format', () => {
  it('writes sizes, months and dates in French, without time zone shifts', () => {
    expect(formatSize(512)).toBe('512 o')
    expect(formatSize(2765)).toBe('2,7 Ko')
    expect(monthLabel('2024-07')).toBe('Juillet 2024')
    expect(wallClock('2024-07-14T18:03:22')).toMatch(/14 juillet 2024.*18:03/)
  })

  it('sorts folders first and groups the timeline by month', () => {
    expect(sortEntries([entry('b.txt'), entry('Zeta', 'dir'), entry('a10.txt'), entry('a2.txt')]).map((e) => e.name)).toEqual(['Zeta', 'a2.txt', 'a10.txt', 'b.txt'])
    const at = (path: string, date: string): TimelineEntry => ({ ...entry(path), at: date, taken: true })
    expect(byMonth([at('a', '2024-07-14T10:00:00'), at('b', '2024-07-02T10:00:00'), at('c', '2023-03-01T10:00:00')]).map((m) => [m.month, m.items.length])).toEqual([
      ['2024-07', 2],
      ['2023-03', 1],
    ])
    expect(parentOf('Photos/Été/a.jpg')).toBe('Photos/Été')
    expect(parentOf('a.jpg')).toBe('')
  })
})
