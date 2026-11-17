import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { FileList } from './FileList.tsx'
import { thumbURL, type Entry } from './files.ts'

const sha = 'ab'.repeat(32)
const file = (path: string, mime: string): Entry => ({
  path,
  name: path.split('/').pop()!,
  kind: 'file',
  size: 10,
  mime,
  sha256: sha,
  modified_at: '2026-11-17T10:00:00Z',
})

describe('thumbURL', () => {
  it('points the images the workers read at their thumbnail, keyed by content', () => {
    expect(thumbURL(file('Été/a b.jpg', 'image/jpeg'))).toBe('/api/files/thumb?path=%C3%89t%C3%A9%2Fa%20b.jpg&v=abababababababab')
    expect(thumbURL(file('a.png', 'image/png'))).not.toBeNull()
    expect(thumbURL(file('a.webp', 'image/webp'))).not.toBeNull()
  })

  it('has none for other files, HEIC and folders', () => {
    expect(thumbURL(file('a.txt', 'text/plain'))).toBeNull()
    expect(thumbURL(file('a.heic', 'image/heic'))).toBeNull()
    expect(thumbURL({ ...file('a.jpg', 'image/jpeg'), sha256: undefined })).toBeNull()
    expect(thumbURL({ ...file('Photos', 'inode/directory'), kind: 'dir' })).toBeNull()
  })
})

describe('FileList thumbnails', () => {
  const noop = () => {}
  const list = (entries: Entry[]) =>
    render(<FileList entries={entries} onRename={noop} onMove={noop} onTrash={noop} onVersions={noop} />)

  it('shows the thumbnail lazily, and the icon again when there is none yet', () => {
    const { container } = list([file('a.jpg', 'image/jpeg'), file('notes.txt', 'text/plain')])
    const imgs = container.querySelectorAll('img.thumb')
    expect(imgs).toHaveLength(1)
    expect(imgs[0].getAttribute('loading')).toBe('lazy')
    expect(imgs[0].getAttribute('alt')).toBe('')
    fireEvent.error(imgs[0])
    expect(container.querySelectorAll('img.thumb')).toHaveLength(0)
    expect(screen.getByText('a.jpg')).toBeTruthy()
  })
})
