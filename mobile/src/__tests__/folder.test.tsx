import { fireEvent, render, screen, waitFor } from '@testing-library/react-native'
import { Folder } from '../components/Folder'
import type { Entry } from '../../../shared/api-types'

const mockPush = jest.fn()
jest.mock('expo-router', () => ({ router: { push: (...a: unknown[]) => mockPush(...a) } }))
jest.mock('expo-symbols', () => ({ SymbolView: () => null }))

const mockEntries: Entry[] = [
  { path: 'Photos/notes.txt', name: 'notes.txt', kind: 'file', size: 2765, mime: 'text/plain', modified_at: '2026-11-17T10:00:00Z' },
  { path: 'Photos/Été', name: 'Été', kind: 'dir', size: 0, mime: 'inode/directory', modified_at: '2026-11-17T10:00:00Z' },
]
const mockApi = {
  list: jest.fn(async () => mockEntries),
  thumbURL: () => null,
  contentURL: (p: string) => `http://box/mockApi/files/content?path=${p}`,
  headers: {},
}
jest.mock('../lib/session', () => ({ useApi: () => mockApi }))

describe('Folder', () => {
  it('lists folders first, and opens a subfolder', async () => {
    render(<Folder path="Photos" />)
    await waitFor(() => expect(screen.getByLabelText('Été')).toBeTruthy())
    expect(mockApi.list).toHaveBeenCalledWith('Photos')
    expect(screen.getByText('2,7 Ko')).toBeTruthy()
    fireEvent.press(screen.getByLabelText('Été'))
    expect(mockPush).toHaveBeenCalledWith('/files/Photos/%C3%89t%C3%A9')
  })

  it('shows the box’s error', async () => {
    mockApi.list.mockRejectedValueOnce(new Error('La box ne répond pas à cette adresse.'))
    render(<Folder path="" />)
    await waitFor(() => expect(screen.getByText('La box ne répond pas à cette adresse.')).toBeTruthy())
  })
})
