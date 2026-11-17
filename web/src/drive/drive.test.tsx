import { act, fireEvent, render, screen, within } from '@testing-library/react'
import type * as tus from 'tus-js-client'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { driveURL, folderFromURL } from '../router.ts'
import { mockApi } from '../test-api.ts'
import { Drive } from './Drive.tsx'
import { formatSize, sortEntries, type Entry } from './files.ts'
import { UploadPanel } from './UploadPanel.tsx'
import { UploadQueue, uploadError, type TusFactory } from './uploads.ts'

const entry = (path: string, kind: 'file' | 'dir', size = 0, modified = '2026-11-17T10:00:00Z', mime = 'text/plain'): Entry => ({
  path,
  name: path.split('/').pop()!,
  kind,
  size,
  mime: kind === 'dir' ? 'inode/directory' : mime,
  modified_at: modified,
})

afterEach(() => {
  vi.unstubAllGlobals()
  window.history.replaceState(null, '', '/')
})

describe('router', () => {
  it('maps folders to drive URLs and back, accents and spaces included', () => {
    expect(driveURL('')).toBe('/drive')
    expect(driveURL('Photos/Été 2024')).toBe('/drive/Photos/%C3%89t%C3%A9%202024')
    expect(folderFromURL('/drive/Photos/%C3%89t%C3%A9%202024')).toBe('Photos/Été 2024')
    expect(folderFromURL('/drive')).toBe('')
    expect(folderFromURL('/drive/')).toBe('')
    expect(folderFromURL('/comptes')).toBeNull()
    expect(folderFromURL('/drive/%E0%A4%A')).toBeNull()
  })
})

describe('sortEntries', () => {
  const list = [
    entry('photo10.jpg', 'file', 300, '2026-01-03T00:00:00Z'),
    entry('photo2.jpg', 'file', 100, '2026-01-01T00:00:00Z'),
    entry('Zeta', 'dir'),
    entry('alpha', 'dir'),
  ]
  const names = (l: Entry[]) => l.map((e) => e.name)

  it('puts folders first and orders names as people read them', () => {
    expect(names(sortEntries(list, 'name', true))).toEqual(['alpha', 'Zeta', 'photo2.jpg', 'photo10.jpg'])
    expect(names(sortEntries(list, 'name', false))).toEqual(['Zeta', 'alpha', 'photo10.jpg', 'photo2.jpg'])
  })

  it('sorts by size and by date', () => {
    expect(names(sortEntries(list, 'size', false)).slice(2)).toEqual(['photo10.jpg', 'photo2.jpg'])
    expect(names(sortEntries(list, 'modified', true)).slice(2)).toEqual(['photo2.jpg', 'photo10.jpg'])
  })

  it('formats sizes in French units', () => {
    expect(formatSize(0)).toBe('0 o')
    expect(formatSize(1536)).toBe('1,5 Ko')
    expect(formatSize(3 * 1024 ** 3)).toBe('3 Go')
  })
})

/** A stand-in for tus.Upload that the test drives by hand. */
function fakeTus() {
  const made: { file: File; options: tus.UploadOptions; started: number; aborted: boolean[] }[] = []
  const factory: TusFactory = (file, options) => {
    const rec = { file, options, started: 0, aborted: [] as boolean[] }
    made.push(rec)
    return {
      start: () => {
        rec.started++
      },
      abort: async (terminate = false) => {
        rec.aborted.push(terminate)
      },
      findPreviousUploads: async () => [],
      resumeFromPreviousUpload: () => {},
    }
  }
  return { made, factory }
}

const file = (name: string, size: number) => new File([new Uint8Array(size)], name, { lastModified: 1_700_000_000_000 })

describe('UploadQueue', () => {
  it('sends two files at a time, to the right path, and resumes a paused one', async () => {
    const { made, factory } = fakeTus()
    const queue = new UploadQueue(factory)
    const stored: string[] = []
    queue.onStored((folder) => stored.push(folder))

    queue.add([file('a.mp4', 10), file('b.mp4', 10), file('c.mp4', 10)], 'Vidéos')
    await vi.waitFor(() => expect(made).toHaveLength(2))
    expect(queue.snapshot().map((it) => it.state)).toEqual(['sending', 'sending', 'waiting'])
    expect(made[0].options.metadata).toEqual({ path: 'Vidéos/a.mp4', filename: 'a.mp4', mtime: '1700000000' })
    expect(made[0].options.headers).toEqual({ 'X-Cloudos-Client': 'web' })

    // Pausing the first lets the third start; resuming the first restarts the same upload.
    queue.pause(1)
    await vi.waitFor(() => expect(made).toHaveLength(3))
    expect(made[0].aborted).toEqual([false])
    act(() => made[1].options.onSuccess?.({} as tus.OnSuccessPayload))
    queue.resume(1)
    await vi.waitFor(() => expect(made[0].started).toBe(2))
    expect(stored).toEqual(['Vidéos'])
  })

  it('cancels with tus termination and reports failures in French', async () => {
    const { made, factory } = fakeTus()
    const queue = new UploadQueue(factory)
    queue.add([file('a.bin', 5), file('b.bin', 5)], '')
    await vi.waitFor(() => expect(made).toHaveLength(2))

    queue.cancel(1)
    expect(made[0].aborted).toEqual([true])
    const conflict = { originalResponse: { getStatus: () => 409 } } as unknown as tus.DetailedError
    act(() => made[1].options.onError?.(conflict))
    expect(queue.snapshot().map((it) => [it.state, it.error])).toEqual([
      ['cancelled', undefined],
      ['failed', 'Un fichier ou un dossier porte déjà ce nom.'],
    ])
    expect(uploadError(new Error('network'))).toBe('Envoi impossible pour le moment.')

    render(<UploadPanel queue={queue} />)
    expect(screen.getByText('Un fichier ou un dossier porte déjà ce nom.')).toBeInTheDocument()
    fireEvent.click(screen.getByText('Effacer'))
    expect(queue.snapshot()).toHaveLength(0)
  })
})

describe('Drive', () => {
  const listing = [entry('Photos/2024', 'dir'), entry('Photos/chien.jpg', 'file', 2048, '2026-11-17T10:00:00Z', 'image/jpeg')]

  it('lists the folder with its breadcrumb, and opens a subfolder', async () => {
    mockApi({ 'GET /api/files?path=Photos': { status: 200, body: listing } })
    render(<Drive folder="Photos" queue={new UploadQueue(fakeTus().factory)} />)

    expect(await screen.findByText('chien.jpg')).toBeInTheDocument()
    const crumbs = screen.getByRole('navigation', { name: "Fil d'Ariane" })
    expect(within(crumbs).getByText('Photos')).toHaveAttribute('aria-current', 'page')
    expect(screen.getByLabelText('Télécharger chien.jpg')).toHaveAttribute('href', '/api/files/content?path=Photos%2Fchien.jpg')

    fireEvent.click(screen.getByText('2024'))
    expect(window.location.pathname).toBe('/drive/Photos/2024')
  })

  it('says when the folder does not exist', async () => {
    mockApi({ 'GET /api/files?path=Absent': { status: 404, body: { error: 'no such file or folder' } } })
    render(<Drive folder="Absent" queue={new UploadQueue(fakeTus().factory)} />)
    expect(await screen.findByText(/Ce dossier n'existe pas/)).toBeInTheDocument()
  })

  it('creates a folder, renames and moves an entry, then reloads the listing', async () => {
    const calls = mockApi({
      'GET /api/files?path=Photos': { status: 200, body: listing },
      'GET /api/files?path=': { status: 200, body: [entry('Photos', 'dir'), entry('Archives', 'dir')] },
      'POST /api/files/folders': { status: 201, body: entry('Photos/Vacances', 'dir') },
      'POST /api/files/move': { status: 200, body: entry('Photos/rex.jpg', 'file') },
    })
    render(<Drive folder="Photos" queue={new UploadQueue(fakeTus().factory)} />)
    await screen.findByText('chien.jpg')

    fireEvent.click(screen.getByText('Nouveau dossier'))
    fireEvent.change(screen.getByLabelText('Nom'), { target: { value: 'Vacances' } })
    fireEvent.click(screen.getByText('Créer'))
    await vi.waitFor(() => expect(calls.some((c) => c.key === 'POST /api/files/folders')).toBe(true))
    expect(calls.find((c) => c.key === 'POST /api/files/folders')?.body).toEqual({ path: 'Photos/Vacances' })

    fireEvent.click(screen.getByLabelText('Renommer chien.jpg'))
    fireEvent.change(screen.getByLabelText('Nom'), { target: { value: 'rex.jpg' } })
    fireEvent.click(screen.getByText('Renommer'))
    await vi.waitFor(() => expect(calls.filter((c) => c.key === 'POST /api/files/move')).toHaveLength(1))
    expect(calls.find((c) => c.key === 'POST /api/files/move')?.body).toEqual({ from: 'Photos/chien.jpg', to: 'Photos/rex.jpg' })

    // Move: go up to the root, pick Archives, move there.
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    fireEvent.click(screen.getByLabelText('Déplacer chien.jpg'))
    fireEvent.click(screen.getByLabelText('Dossier parent'))
    fireEvent.click(await screen.findByText('Archives'))
    await vi.waitFor(() => expect(calls.some((c) => c.key === 'GET /api/files?path=Archives')).toBe(true))
    fireEvent.click(screen.getByText('Déplacer ici'))
    await vi.waitFor(() => expect(calls.filter((c) => c.key === 'POST /api/files/move')).toHaveLength(2))
    expect(calls.filter((c) => c.key === 'POST /api/files/move')[1].body).toEqual({ from: 'Photos/chien.jpg', to: 'Archives/chien.jpg' })
    expect(calls.filter((c) => c.key === 'GET /api/files?path=Photos').length).toBeGreaterThanOrEqual(3)
  })

  it('queues dropped files for the current folder', async () => {
    mockApi({ 'GET /api/files?path=Photos': { status: 200, body: listing } })
    const { made, factory } = fakeTus()
    render(<Drive folder="Photos" queue={new UploadQueue(factory)} />)
    await screen.findByText('chien.jpg')

    fireEvent.change(screen.getByTestId('file-picker'), { target: { files: [file('plage.jpg', 3)] } })
    await vi.waitFor(() => expect(made).toHaveLength(1))
    expect(made[0].options.metadata?.path).toBe('Photos/plage.jpg')
    expect(await screen.findByText('1 envoi en cours')).toBeInTheDocument()
  })
})
