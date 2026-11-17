import { act, fireEvent, render, screen } from '@testing-library/react'
import type * as tus from 'tus-js-client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi } from '../test-api.ts'
import { VersionsDialog } from './Dialogs.tsx'
import type { Entry, VersionEntry } from './files.ts'
import { UploadPanel } from './UploadPanel.tsx'
import { UploadQueue, shouldRetry, type TusFactory } from './uploads.ts'

const plan: Entry = { path: 'Docs/plan.txt', name: 'plan.txt', kind: 'file', size: 3, mime: 'text/plain', modified_at: '2026-11-17T10:00:00Z' }
const v = (id: number, modified: string): VersionEntry => ({
  id,
  size: 2,
  mime: 'text/plain',
  sha256: 'ab',
  modified_at: modified,
  replaced_at: '2026-11-17T10:00:00Z',
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('VersionsDialog', () => {
  it('lists the versions, links each download, and restores one', async () => {
    const onRestored = vi.fn()
    const calls = mockApi({
      'GET /api/files/versions?path=Docs%2Fplan.txt': [
        { status: 200, body: [v(5, '2026-11-16T09:00:00Z'), v(4, '2026-11-15T09:00:00Z')] },
        { status: 200, body: [v(6, '2026-11-17T10:00:00Z'), v(5, '2026-11-16T09:00:00Z'), v(4, '2026-11-15T09:00:00Z')] },
      ],
      'POST /api/files/versions/4/restore': { status: 200, body: plan },
    })
    render(<VersionsDialog entry={plan} onRestored={onRestored} onClose={() => {}} />)

    const downloads = await screen.findAllByTitle('Télécharger')
    expect(downloads).toHaveLength(2)
    expect(downloads[1]).toHaveAttribute('href', '/api/files/versions/4/content?name=plan.txt')

    fireEvent.click(screen.getAllByTitle('Restaurer')[1])
    expect(await screen.findByText(/Version restaurée/)).toBeInTheDocument()
    expect(onRestored).toHaveBeenCalled()
    expect(calls.some((c) => c.key === 'POST /api/files/versions/4/restore')).toBe(true)
    // The list reloads: the replaced content is now a version too.
    await vi.waitFor(() => expect(screen.getAllByTitle('Restaurer')).toHaveLength(3))
  })

  it('says when a file was never replaced', async () => {
    mockApi({ 'GET /api/files/versions?path=Docs%2Fplan.txt': { status: 200, body: [] } })
    render(<VersionsDialog entry={plan} onRestored={() => {}} onClose={() => {}} />)
    expect(await screen.findByText(/jamais été remplacé/)).toBeInTheDocument()
  })
})

describe('replacing from the upload panel', () => {
  it('offers Remplacer when the name is taken, then sends with replace', async () => {
    const made: tus.UploadOptions[] = []
    const factory: TusFactory = (_file, options) => {
      made.push(options)
      return { start() {}, abort: async () => {}, findPreviousUploads: async () => [], resumeFromPreviousUpload() {} }
    }
    const queue = new UploadQueue(factory)
    queue.add([new File(['abc'], 'plan.txt')], 'Docs')
    await vi.waitFor(() => expect(made).toHaveLength(1))
    expect(made[0].metadata?.replace).toBeUndefined()

    const conflict = { originalResponse: { getStatus: () => 409 } } as unknown as tus.DetailedError
    act(() => made[0].onError?.(conflict))
    render(<UploadPanel queue={queue} />)
    fireEvent.click(screen.getByText('Remplacer'))

    await vi.waitFor(() => expect(made).toHaveLength(2))
    expect(made[1].metadata?.replace).toBe('1')
    expect(made[1].metadata?.path).toBe('Docs/plan.txt')
    expect(screen.queryByText('Remplacer')).toBeNull()
  })
})

describe('shouldRetry', () => {
  const err = (status: number | undefined, method = 'POST') =>
    ({
      originalResponse: status === undefined ? undefined : { getStatus: () => status },
      originalRequest: { getMethod: () => method },
    }) as unknown as tus.DetailedError

  it('retries cuts, server errors, a busy upload and a chunk out of step', () => {
    expect(shouldRetry(err(undefined))).toBe(true)
    expect(shouldRetry(err(502, 'PATCH'))).toBe(true)
    expect(shouldRetry(err(423, 'PATCH'))).toBe(true)
    expect(shouldRetry(err(409, 'PATCH'))).toBe(true)
  })

  it('stops at once on a refused creation', () => {
    expect(shouldRetry(err(409, 'POST'))).toBe(false) // name taken: offer Remplacer
    expect(shouldRetry(err(413, 'POST'))).toBe(false)
    expect(shouldRetry(err(400, 'POST'))).toBe(false)
    expect(shouldRetry(err(422, 'PATCH'))).toBe(false)
    expect(shouldRetry(err(401, 'PATCH'))).toBe(false)
  })
})
