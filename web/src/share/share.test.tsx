import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Entry } from '../drive/files.ts'
import { mockApi } from '../test-api.ts'
import { ShareDialog } from './ShareDialog.tsx'
import { SharePage } from './SharePage.tsx'
import { expiryOf, shareOrigin, shareURL, tokenFromURL, type GuestItem, type ShareEntry } from './shares.ts'
import { SharesPage } from './SharesPage.tsx'

const token = 'A'.repeat(43)

const entry = (path: string, kind: 'file' | 'dir' = 'file'): Entry => ({
  path,
  name: path.split('/').pop()!,
  kind,
  size: 2048,
  mime: kind === 'dir' ? 'inode/directory' : 'image/jpeg',
  modified_at: '2026-11-17T10:00:00Z',
})

const link = (id: number, path: string, extra: Partial<ShareEntry> = {}): ShareEntry => ({
  id,
  path,
  name: path.split('/').pop()!,
  kind: 'dir',
  size: 0,
  has_password: false,
  expires_at: '2026-11-24T10:00:00Z',
  created_at: '2026-11-17T10:00:00Z',
  expired: false,
  trashed: false,
  ...extra,
})

const folder: GuestItem = { name: 'Vacances', kind: 'dir', size: 0, mime: 'inode/directory', modified_at: '2026-11-17T10:00:00Z', expires_at: '2026-11-24T10:00:00Z' }

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('share helpers', () => {
  it('reads the token of a guest address only', () => {
    expect(tokenFromURL(`/s/${token}`)).toBe(token)
    expect(tokenFromURL(`/s/${token}/`)).toBe(token)
    expect(tokenFromURL('/drive/s/abc')).toBeNull()
    expect(tokenFromURL('/s/')).toBeNull()
    expect(tokenFromURL('/s/a/b')).toBeNull()
  })

  it('turns an expiry choice into a date', () => {
    const now = Date.parse('2026-11-17T10:00:00Z')
    expect(expiryOf('7', '', now)?.toISOString()).toBe('2026-11-24T10:00:00.000Z')
    const day = expiryOf('date', '2027-03-01', now)!
    expect([day.getFullYear(), day.getMonth(), day.getDate(), day.getHours()]).toEqual([2027, 2, 1, 23])
    expect(expiryOf('date', '', now)).toBeNull()
  })
})

describe('shareURL and shareOrigin', () => {
  it('builds the guest address on the given origin, or this page\'s', () => {
    expect(shareURL(token)).toBe(`${window.location.origin}/s/${token}`)
    expect(shareURL(token, 'https://cloudos.tailnet.ts.net:8443')).toBe(`https://cloudos.tailnet.ts.net:8443/s/${token}`)
  })

  it('takes the box\'s share origin when Funnel is on', async () => {
    mockApi({ 'GET /api/config': { status: 200, body: { share_origin: 'https://cloudos.tailnet.ts.net:8443' } } })
    expect(await shareOrigin('https://cloudos.tailnet.ts.net')).toBe('https://cloudos.tailnet.ts.net:8443')
  })

  it('keeps this page\'s origin without Funnel, or when the core does not say', async () => {
    mockApi({ 'GET /api/config': { status: 200, body: { share_origin: '' } } })
    expect(await shareOrigin('https://cloudos.tailnet.ts.net')).toBe('https://cloudos.tailnet.ts.net')
    mockApi({ 'GET /api/config': { status: 500 } })
    expect(await shareOrigin('http://localhost:8480')).toBe('http://localhost:8480')
  })
})

describe('ShareDialog', () => {
  it('shows the link on the public share address when Funnel is on', async () => {
    mockApi({
      'POST /api/shares': { status: 201, body: { share: link(3, 'plage.jpg'), token } },
      'GET /api/config': { status: 200, body: { share_origin: 'https://cloudos.tailnet.ts.net:8443' } },
    })
    render(<ShareDialog entry={entry('plage.jpg')} onClose={() => {}} />)
    fireEvent.click(screen.getByText('Créer le lien'))
    const field = await screen.findByLabelText('Lien de partage')
    expect((field as HTMLInputElement).value).toBe(`https://cloudos.tailnet.ts.net:8443/s/${token}`)
  })

  it('creates a 7-day link by default and shows it once, ready to copy', async () => {
    const calls = mockApi({ 'POST /api/shares': { status: 201, body: { share: link(1, 'Photos/Vacances'), token } } })
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    render(<ShareDialog entry={entry('Photos/Vacances', 'dir')} onClose={() => {}} />)

    expect(screen.getByText(/ce dossier et son contenu/)).toBeInTheDocument()
    fireEvent.click(screen.getByText('Créer le lien'))
    const field = await screen.findByLabelText('Lien de partage')
    expect((field as HTMLInputElement).value).toBe(`${window.location.origin}/s/${token}`)
    expect(screen.getByText(/il ne sera plus affiché/)).toBeInTheDocument()

    const body = calls[0].body as { path: string; expires_at: string; password: string }
    expect(body.path).toBe('Photos/Vacances')
    expect(body.password).toBe('')
    const days = (Date.parse(body.expires_at) - Date.now()) / 86_400_000
    expect(days).toBeGreaterThan(6.9)
    expect(days).toBeLessThan(7.1)

    fireEvent.click(screen.getByText('Copier le lien'))
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith(`${window.location.origin}/s/${token}`))
    expect(await screen.findByText('Copié')).toBeInTheDocument()
  })

  it('refuses a short password without calling the core', async () => {
    const calls = mockApi({})
    render(<ShareDialog entry={entry('plage.jpg')} onClose={() => {}} />)
    fireEvent.click(screen.getByLabelText('Protéger par un mot de passe'))
    fireEvent.change(screen.getByLabelText('Mot de passe du lien'), { target: { value: 'abc' } })
    fireEvent.submit(screen.getByText('Créer le lien').closest('form')!)
    expect(await screen.findByText(/au moins 6 caractères/)).toBeInTheDocument()
    expect(calls).toHaveLength(0)
  })

  it('sends the password and a chosen day', async () => {
    const calls = mockApi({ 'POST /api/shares': { status: 201, body: { share: link(2, 'plage.jpg'), token } } })
    render(<ShareDialog entry={entry('plage.jpg')} onClose={() => {}} />)
    fireEvent.click(screen.getByLabelText('Jusqu’au…'))
    fireEvent.change(screen.getByLabelText('Date d’expiration'), { target: { value: '2999-01-31' } })
    fireEvent.click(screen.getByLabelText('Protéger par un mot de passe'))
    fireEvent.change(screen.getByLabelText('Mot de passe du lien'), { target: { value: 'sesame ouvre-toi' } })
    fireEvent.click(screen.getByText('Créer le lien'))
    await screen.findByLabelText('Lien de partage')
    const body = calls[0].body as { expires_at: string; password: string }
    expect(body.password).toBe('sesame ouvre-toi')
    expect(new Date(body.expires_at).getFullYear()).toBe(2999)
  })
})

describe('SharesPage', () => {
  it('lists links with their state and revokes one after a confirmation', async () => {
    const calls = mockApi({
      'GET /api/shares': [
        {
          status: 200,
          body: [
            link(3, 'Photos/Vacances', { has_password: true }),
            link(2, 'contrat.pdf', { kind: 'file', trashed: true }),
            link(1, 'Docs/ancien.txt', { kind: 'file', expired: true }),
          ],
        },
        { status: 200, body: [link(2, 'contrat.pdf', { kind: 'file', trashed: true })] },
      ],
      'DELETE /api/shares/3': { status: 204 },
    })
    render(<SharesPage />)
    expect(await screen.findByText('Vacances')).toBeInTheDocument()
    expect(screen.getByText('Mot de passe')).toBeInTheDocument()
    expect(screen.getByText(/Dans Photos · Expire le/)).toBeInTheDocument()
    expect(screen.getByText(/Dans la corbeille : le lien ne donne rien/)).toBeInTheDocument()
    expect(screen.getByText(/Dans Docs · Expiré le/)).toBeInTheDocument()

    fireEvent.click(screen.getByLabelText('Révoquer le lien vers Vacances'))
    expect(screen.getByText(/ne donnera plus rien/)).toBeInTheDocument()
    fireEvent.click(screen.getByText('Révoquer'))
    expect(await screen.findByText('Lien vers « Vacances » révoqué.')).toBeInTheDocument()
    expect(calls.some((c) => c.key === 'DELETE /api/shares/3')).toBe(true)
    await vi.waitFor(() => expect(screen.queryByText('Vacances')).not.toBeInTheDocument())
  })

  it('says when there is no link', async () => {
    mockApi({ 'GET /api/shares': { status: 200, body: [] } })
    render(<SharesPage />)
    expect(await screen.findByText(/Aucun lien de partage/)).toBeInTheDocument()
  })
})

describe('SharePage (guest)', () => {
  const base = `/api/s/${token}`

  it('says a dead link gives nothing', async () => {
    mockApi({ [`GET ${base}`]: { status: 404, body: { error: 'no such link' } } })
    render(<SharePage token={token} />)
    expect(await screen.findByText('Lien indisponible')).toBeInTheDocument()
  })

  it('asks for the password, then browses the folder and downloads', async () => {
    const calls = mockApi({
      [`GET ${base}`]: [{ status: 403, body: { error: 'password required' } }, { status: 200, body: folder }],
      [`POST ${base}/unlock`]: [{ status: 403, body: { error: 'wrong password' } }, { status: 204 }],
      [`GET ${base}/files?path=`]: { status: 200, body: [entry('2024', 'dir'), entry('plage.jpg')] },
      [`GET ${base}/files?path=2024`]: { status: 200, body: [entry('2024/chien.jpg')] },
    })
    render(<SharePage token={token} />)
    expect(await screen.findByText('Lien protégé')).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText('Mot de passe'), { target: { value: 'mauvais' } })
    fireEvent.click(screen.getByText('Ouvrir'))
    expect(await screen.findByText('Mot de passe incorrect.')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Mot de passe'), { target: { value: 'sesame ouvre-toi' } })
    fireEvent.click(screen.getByText('Ouvrir'))

    expect(await screen.findByText('plage.jpg')).toBeInTheDocument()
    expect(calls.filter((c) => c.key === `POST ${base}/unlock`).map((c) => c.body)).toEqual([{ password: 'mauvais' }, { password: 'sesame ouvre-toi' }])
    expect(screen.getByLabelText('Télécharger plage.jpg').getAttribute('href')).toBe(`${base}/content?path=plage.jpg`)
    expect(screen.getByText('Tout télécharger (zip)').closest('a')?.getAttribute('href')).toBe(`${base}/zip?path=`)

    fireEvent.click(screen.getByText('2024'))
    expect(await screen.findByText('chien.jpg')).toBeInTheDocument()
    expect(screen.getByLabelText('Télécharger chien.jpg').getAttribute('href')).toBe(`${base}/content?path=2024%2Fchien.jpg`)
    expect(screen.getByText('Tout télécharger (zip)').closest('a')?.getAttribute('href')).toBe(`${base}/zip?path=2024`)
    // No write action is offered to a guest.
    expect(screen.queryByLabelText(/Supprimer|Renommer|Déplacer|Partager/)).not.toBeInTheDocument()
  })

  it('shows a shared file with its download button', async () => {
    mockApi({ [`GET ${base}`]: { status: 200, body: { ...folder, name: 'contrat.pdf', kind: 'file', size: 4096, mime: 'application/pdf' } } })
    render(<SharePage token={token} />)
    expect(await screen.findByText('contrat.pdf')).toBeInTheDocument()
    expect(screen.getByText('Télécharger').closest('a')?.getAttribute('href')).toBe(`${base}/content?path=`)
  })
})
