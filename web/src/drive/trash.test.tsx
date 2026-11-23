import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi } from '../test-api.ts'
import { Drive } from './Drive.tsx'
import { inDays, type Entry, type TrashEntry } from './files.ts'
import { TrashPage } from './TrashPage.tsx'
import { UploadQueue } from './uploads.ts'

const file = (path: string): Entry => ({
  path,
  name: path.split('/').pop()!,
  kind: 'file',
  size: 2048,
  mime: 'application/pdf',
  modified_at: '2026-11-17T10:00:00Z',
})

const item = (id: number, path: string, kind: 'file' | 'dir' = 'file', size = 2048): TrashEntry => ({
  id,
  path,
  name: path.split('/').pop()!,
  kind,
  size,
  deleted_at: '2026-11-17T10:00:00Z',
  expires_at: '2026-12-17T10:00:00Z',
})

const noUploads = () =>
  new UploadQueue(() => ({ start() {}, abort: async () => {}, findPreviousUploads: async () => [], resumeFromPreviousUpload() {} }))

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('trash in the drive', () => {
  it('moves an entry to the trash without asking, and undoes it from the notice', async () => {
    const calls = mockApi({
      'GET /api/files?path=Docs': { status: 200, body: [file('Docs/facture.pdf')] },
      'POST /api/files/trash': { status: 200, body: item(7, 'Docs/facture.pdf') },
      'POST /api/trash/7/restore': { status: 200, body: file('Docs/facture.pdf') },
    })
    render(<Drive folder="Docs" queue={noUploads()} />)
    fireEvent.click(await screen.findByLabelText('Supprimer facture.pdf'))

    expect(await screen.findByText(/mis à la corbeille/)).toBeInTheDocument()
    expect(calls.find((c) => c.key === 'POST /api/files/trash')?.body).toEqual({ path: 'Docs/facture.pdf' })
    fireEvent.click(screen.getByText('Annuler'))
    await vi.waitFor(() => expect(calls.some((c) => c.key === 'POST /api/trash/7/restore')).toBe(true))
  })
})

describe('TrashPage', () => {
  it('lists items with where they were, and restores one', async () => {
    mockApi({
      'GET /api/trash': [
        { status: 200, body: [item(3, 'Projets/Box', 'dir', 4096), item(4, 'notes.txt')] },
        { status: 200, body: [item(4, 'notes.txt')] },
      ],
      'POST /api/trash/3/restore': { status: 200, body: { ...file('Projets/Box (restauré)'), kind: 'dir' } },
    })
    render(<TrashPage />)
    expect(await screen.findByText('Box')).toBeInTheDocument()
    expect(screen.getByText(/Était dans Projets/)).toBeInTheDocument()
    expect(screen.getByText(/Était dans Mon drive/)).toBeInTheDocument()

    fireEvent.click(screen.getByLabelText('Restaurer Box'))
    expect(await screen.findByText(/restauré dans Projets sous le nom « Box \(restauré\) »/)).toBeInTheDocument()
  })

  it('says which items the box kept itself, from an SMB write it could not store', async () => {
    mockApi({
      'GET /api/trash': { status: 200, body: [{ ...item(5, 'Docs/rapport.txt'), reason: 'smb_not_saved' }, item(6, 'notes.txt')] },
    })
    render(<TrashPage />)
    expect(await screen.findByText('rapport.txt')).toBeInTheDocument()
    expect(screen.getAllByText('Gardé : l’enregistrement en SMB a échoué')).toHaveLength(1)
    expect(screen.getByText(/gardé le/)).toBeInTheDocument()
    expect(screen.getByText(/supprimé le/)).toBeInTheDocument()
  })

  it('erases one item or the whole trash only after a confirmation', async () => {
    const calls = mockApi({
      'GET /api/trash': [
        { status: 200, body: [item(3, 'a.pdf'), item(4, 'b.pdf')] },
        { status: 200, body: [item(4, 'b.pdf')] },
        { status: 200, body: [] },
      ],
      'POST /api/trash/3/purge': { status: 204 },
      'POST /api/trash/empty': { status: 200, body: { purged: 1 } },
    })
    render(<TrashPage />)
    fireEvent.click(await screen.findByLabelText('Supprimer définitivement a.pdf'))
    expect(screen.getByRole('dialog', { name: 'Supprimer définitivement ?' })).toBeInTheDocument()

    // Cancelling sends nothing.
    fireEvent.click(screen.getByText('Annuler'))
    expect(calls.some((c) => c.key.includes('/purge'))).toBe(false)

    fireEvent.click(screen.getByLabelText('Supprimer définitivement a.pdf'))
    fireEvent.click(screen.getAllByText('Supprimer définitivement').at(-1)!)
    await vi.waitFor(() => expect(calls.some((c) => c.key === 'POST /api/trash/3/purge')).toBe(true))
    expect(calls.find((c) => c.key === 'POST /api/trash/3/purge')?.body).toEqual({ confirm: true })

    await vi.waitFor(() => expect(screen.queryByText('a.pdf')).toBeNull())
    fireEvent.click(screen.getByText('Vider la corbeille'))
    expect(screen.getByRole('dialog', { name: 'Vider la corbeille ?' })).toHaveTextContent(/L’élément de la corbeille \(2\sKo\) sera effacé de la box/)
    fireEvent.click(screen.getAllByText('Vider la corbeille').at(-1)!)
    expect(await screen.findByText('La corbeille est vide.')).toBeInTheDocument()
    expect(calls.find((c) => c.key === 'POST /api/trash/empty')?.body).toEqual({ confirm: true })
  })

  it('says when an item will be erased', () => {
    const now = Date.parse('2026-11-17T10:00:00Z')
    expect(inDays('2026-12-17T10:00:00Z', now)).toBe('dans 30 jours')
    expect(inDays('2026-11-18T09:00:00Z', now)).toBe('demain')
    expect(inDays('2026-11-17T09:00:00Z', now)).toBe("aujourd'hui")
  })
})
