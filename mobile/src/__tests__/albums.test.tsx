import { fireEvent, render, screen, waitFor } from '@testing-library/react-native'
import type { Album, TimelineEntry } from '../../../shared/api-types'
import { AlbumList } from '../components/AlbumList'
import { PhotoGrid } from '../components/PhotoGrid'

jest.mock('expo-symbols', () => ({ SymbolView: () => null }))

const plage: TimelineEntry = {
  path: 'Photos/plage.jpg',
  name: 'plage.jpg',
  kind: 'file',
  size: 2048,
  mime: 'image/jpeg',
  sha256: 'ab'.repeat(32),
  modified_at: '2026-11-17T10:00:00Z',
  at: '2024-07-14T18:03:22',
  taken: true,
}
const mockAlbums: Album[] = [
  { id: 7, name: 'Bretagne', created_at: '2026-11-18T09:00:00Z', count: 2, cover: plage },
  { id: 8, name: 'Vide', created_at: '2026-11-18T09:00:00Z', count: 0, cover: null },
]
const mockApi = {
  albums: jest.fn(async () => mockAlbums),
  thumbURL: () => 'http://box/thumb',
  headers: {},
}
jest.mock('../lib/session', () => ({ useApi: () => mockApi }))

describe('AlbumList', () => {
  it('lists the albums with their count and opens one', async () => {
    const onOpen = jest.fn()
    await render(<AlbumList onOpen={onOpen} />)
    await waitFor(() => expect(screen.getByLabelText('Bretagne, 2 éléments')).toBeTruthy())
    expect(screen.getByLabelText('Vide, 0 élément')).toBeTruthy()
    await fireEvent.press(screen.getByLabelText('Bretagne, 2 éléments'))
    expect(onOpen).toHaveBeenCalledWith(mockAlbums[0])
  })
})

describe('PhotoGrid', () => {
  it('shows an album’s photos by month, and says when it is empty', async () => {
    const fetchPage = jest.fn(async () => ({ items: [plage], next: null }))
    await render(<PhotoGrid fetchPage={fetchPage} empty="Cet album est vide." />)
    await waitFor(() => expect(screen.getByLabelText('plage.jpg')).toBeTruthy())
    expect(screen.getByText('Juillet 2024')).toBeTruthy()
    expect(fetchPage).toHaveBeenCalledWith(null)

    await render(<PhotoGrid fetchPage={async () => ({ items: [], next: null })} empty="Cet album est vide." />)
    await waitFor(() => expect(screen.getByText('Cet album est vide.')).toBeTruthy())
  })
})
