import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { FileList } from '../drive/FileList.tsx'
import type { Entry } from '../drive/files.ts'
import { Palette } from '../desktop/Palette.tsx'
import { DocViewer } from './DocViewer.tsx'
import { previewKind, textHead } from './preview.ts'

const rendered: number[] = []
vi.mock('pdfjs-dist', () => ({
  GlobalWorkerOptions: { workerSrc: '' },
  getDocument: (opts: { url: string }) => ({
    promise: Promise.resolve({
      url: opts.url,
      numPages: 3,
      loadingTask: { destroy: async () => {} },
      getPage: async (n: number) => ({
        getViewport: ({ scale }: { scale: number }) => ({ width: 600 * scale, height: 800 * scale }),
        render: () => {
          rendered.push(n)
          return { promise: Promise.resolve(), cancel: () => {} }
        },
      }),
    }),
  }),
}))
vi.mock('pdfjs-dist/build/pdf.worker.min.mjs?url', () => ({ default: '/pdf.worker.mjs' }))

const at = '2026-11-17T10:00:00Z'
const file = (path: string, mime: string, size = 2048): Entry => ({ path, name: path.split('/').pop()!, kind: 'file', size, mime, sha256: 'ab'.repeat(32), modified_at: at })
const pdf = file('Documents/bail.pdf', 'application/pdf')
const notes = file('Documents/notes.md', 'text/markdown')
const photo = file('Photos/plage.jpg', 'image/jpeg')

/** Answers the content of files by HTTP range, as the core does. */
function serve(files: Record<string, string>, json: Record<string, unknown> = {}) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (url in json) return new Response(JSON.stringify(json[url]), { status: 200 })
      const path = decodeURIComponent(new URL(url, 'http://box').searchParams.get('path') ?? '')
      const body = new TextEncoder().encode(files[path] ?? '')
      const range = /bytes=0-(\d+)/.exec(new Headers(init?.headers).get('Range') ?? '')
      if (!range) return new Response(body, { status: 200 })
      const part = body.slice(0, Number(range[1]) + 1)
      return new Response(part, { status: 206, headers: { 'Content-Range': `bytes 0-${part.length - 1}/${body.length}` } })
    }),
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
  rendered.length = 0
})

describe('previewKind', () => {
  it('knows PDFs, drawable images and texts, not the rest', () => {
    expect(previewKind(pdf)).toBe('pdf')
    expect(previewKind(photo)).toBe('image')
    expect(previewKind(notes)).toBe('text')
    expect(previewKind(file('a/data.json', 'application/json'))).toBe('text')
    expect(previewKind(file('a/export.csv', 'application/octet-stream'))).toBe('text') // by its extension
    expect(previewKind(file('a/IMG.HEIC', 'image/heic'))).toBeNull()
    expect(previewKind(file('a/archive.zip', 'application/zip'))).toBeNull()
    expect(previewKind({ ...file('a/Photos', 'inode/directory'), kind: 'dir' })).toBeNull()
  })
})

describe('textHead', () => {
  it('reads only the first bytes, by a range, and says there is more', async () => {
    serve({ 'Documents/notes.md': 'é'.repeat(100) })
    const head = await textHead('Documents/notes.md', 11)
    expect(head.more).toBe(true)
    expect(head.text.startsWith('ééééé')).toBe(true) // a cut character becomes a mark, not an error
    expect(head.text.length).toBe(6)
  })
})

describe('DocViewer', () => {
  it('turns the pages of a PDF and closes with Escape', async () => {
    const onClose = vi.fn()
    render(<DocViewer entry={pdf} onClose={onClose} />)
    expect(await screen.findByText('Page 1 / 3')).toBeInTheDocument()
    await waitFor(() => expect(rendered).toEqual([1]))
    fireEvent.click(screen.getByRole('button', { name: 'Page suivante' }))
    fireEvent.keyDown(window, { key: 'ArrowRight' })
    expect(screen.getByText('Page 3 / 3')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Page suivante' })).toBeDisabled()
    await waitFor(() => expect(rendered.at(-1)).toBe(3))
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalled()
  })

  it('shows a text as plain text, never as HTML', async () => {
    serve({ 'Documents/notes.md': '<img src=x onerror=alert(1)> # Courses\n- pain' })
    render(<DocViewer entry={notes} onClose={() => {}} />)
    expect(await screen.findByText(/# Courses/)).toBeInTheDocument()
    expect(document.querySelector('.doc-text img')).toBeNull()
  })

  it('shows an image in its original', () => {
    render(<DocViewer entry={photo} onClose={() => {}} />)
    expect(screen.getByRole('img', { name: 'plage.jpg' })).toHaveAttribute('src', '/api/files/content?path=Photos%2Fplage.jpg')
  })
})

/** The search field of Demander (the period is a combobox too). */
const box = () => screen.getByRole('combobox', { name: /Chercher/ })
const list = () => screen.getByRole('listbox', { name: 'Résultats' })

describe('Demander and Fichiers', () => {
  it('shows the start of a text in Demander, opens the viewer, and Escape closes the viewer only', async () => {
    const onClose = vi.fn()
    serve({ 'Documents/notes.md': 'Acheter du pain' }, { '/api/search/all?q=notes&limit=30': { results: [{ ...notes, score: 0.9, found_by: ['name'] }], partial: false } })
    render(<Palette onClose={onClose} />)
    fireEvent.change(box(), { target: { value: 'notes' } })
    await within(list()).findAllByRole('option')
    expect(await screen.findByText('Acheter du pain')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: "Ouvrir l'aperçu de notes.md" }))
    expect(screen.getByRole('dialog', { name: 'notes.md' })).toBeInTheDocument()
    fireEvent.keyDown(box(), { key: 'Escape' })
    expect(screen.queryByRole('dialog', { name: 'notes.md' })).not.toBeInTheDocument()
    expect(onClose).not.toHaveBeenCalled()
  })

  it('opens the viewer from a file name in Fichiers', async () => {
    const onPreview = vi.fn()
    const noop = () => {}
    render(<FileList entries={[pdf, file('a/archive.zip', 'application/zip')]} onRename={noop} onMove={noop} onTrash={noop} onVersions={noop} onShare={noop} onPreview={onPreview} />)
    await act(async () => fireEvent.click(screen.getByRole('button', { name: 'bail.pdf' })))
    expect(onPreview).toHaveBeenCalledWith(pdf)
    expect(screen.queryByRole('button', { name: 'archive.zip' })).not.toBeInTheDocument()
  })
})
