import { render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { BoxStatus as Status } from '../../../shared/api-types.ts'
import { formatSize } from '../drive/files.ts'
import { mockApi } from '../test-api.ts'
import { BoxStatus } from './BoxStatus.tsx'
import { alertText, durationLabel, freePercent } from './status.ts'

const GB = 1024 ** 3
// 1 000 Go: French separates thousands with a narrow no-break space.
const thousandGB = formatSize(1000 * GB)

/** A box whose worker stopped, whose last backup failed and that kept an SMB write. */
export const troubled: Status = {
  version: '0.5.0',
  checked_at: '2026-11-23T10:42:05Z',
  data_disk: { total: 1000 * GB, free: 50 * GB },
  backup_disk: { total: 2000 * GB, free: 900 * GB, at: '2026-11-23T03:20:00Z' },
  services: [
    { name: 'core', state: 'ok' },
    { name: 'worker', state: 'down', seen_at: '2026-11-23T10:30:00Z', analysis: 'down', search: 'down' },
    { name: 'smb', state: 'ok' },
    { name: 'tailscale', state: 'off' },
  ],
  access: { public_url: '', funnel: false },
  backup: {
    on: true,
    last: {
      backup: { started_at: '2026-11-23T03:00:00Z', finished_at: '2026-11-23T03:00:02Z', ok: false, state: 'failed', error: 'no restic repository: is the backup disk plugged in?' },
      restore_test: { started_at: '2026-11-22T03:30:00Z', finished_at: '2026-11-22T03:40:00Z', ok: true, state: 'ok', sample: 200, mismatches: 0 },
    },
    last_ok: '2026-11-22T03:20:00Z',
    first_at: '2026-10-01T03:00:00Z',
  },
  smb_kept: { waiting: 0, in_trash: 1, last_at: '2026-11-21T18:00:00Z' },
  alerts: [{ kind: 'data_disk_low' }, { kind: 'service_down', service: 'worker' }, { kind: 'backup_failed' }, { kind: 'smb_kept' }],
}

const health = { version: '0.5.0', db: 'ok', migration: 29, workers: [], tasks: { queued: 0, running: 0, failed: 0 } }

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('État de la box', () => {
  it('shows the alerts, the disks, the services, the backup and the SMB writes kept', async () => {
    mockApi({ 'GET /api/box/status': { status: 200, body: troubled }, 'GET /api/health': { status: 200, body: health } })
    render(<BoxStatus />)

    const alerts = await screen.findByRole('region', { name: 'Alertes' })
    const items = within(alerts).getAllByRole('listitem').map((li) => li.textContent?.trim())
    expect(items).toEqual([
      `Le disque des données est presque plein : 50 Go libres sur ${thousandGB}.`,
      'Le worker (analyse et recherche) ne répond plus.',
      expect.stringMatching(/^La dernière sauvegarde a échoué \(23 nov\. 2026, \d\d:00\)\.$/),
      'Un fichier envoyé par SMB n’a pas pu être enregistré : il est dans la corbeille de son propriétaire.',
    ])

    const disks = screen.getByRole('region', { name: 'Disques' })
    // getByText's normalizer turns every space into a plain one.
    expect(within(disks).getByText(`50 Go libres sur ${thousandGB.replace(/\s/g, ' ')} (5 %)`)).toBeInTheDocument()
    expect(within(disks).getByRole('meter', { name: 'Données : 95 % occupé' })).toBeInTheDocument()
    expect(within(disks).getByText('900 Go libres sur 2 To (45 %)')).toBeInTheDocument()
    expect(within(disks).getByText(/^Mesuré le 23 nov\. 2026/)).toBeInTheDocument()

    const services = screen.getByRole('region', { name: 'Services' })
    expect(within(services).getByText('v0.5.0')).toBeInTheDocument()
    expect(within(services).getAllByText('ne répond plus')).toHaveLength(2) // analysis and search
    expect(within(services).getAllByText('en marche')).toHaveLength(1) // smb
    expect(within(services).getByText('éteint')).toBeInTheDocument() // tailscale

    const backup = screen.getByRole('region', { name: 'Sauvegarde' })
    expect(within(backup).getByText('échoué')).toHaveClass('bad')
    expect(within(backup).getByText('no restic repository: is the backup disk plugged in?')).toBeInTheDocument()
    expect(within(backup).getByText('200 fichiers comparés à l’index, 0 différent')).toBeInTheDocument()
    expect(within(backup).getByText('jamais')).toBeInTheDocument() // no check yet

    const kept = screen.getByRole('region', { name: 'Fichiers SMB gardés' })
    expect(within(kept).getByText('En attente de la base').nextElementSibling).toHaveTextContent('0')
    expect(within(kept).getByText('Mis à la corbeille sur 30 jours').nextElementSibling).toHaveTextContent(/^1 · le dernier le 21 nov\. 2026/)

    // The core's health block, moved here from Comptes.
    const core = screen.getByRole('region', { name: 'Cœur et base' })
    expect(await within(core).findByText('joignable')).toBeInTheDocument()
  })

  it('says all is well when nothing calls for a look', async () => {
    mockApi({
      'GET /api/box/status': { status: 200, body: { ...troubled, alerts: [], backup: { ...troubled.backup, on: false } } },
      'GET /api/health': { status: 200, body: health },
    })
    render(<BoxStatus />)
    expect(await screen.findByText('Tout va bien.')).toBeInTheDocument()
    expect(screen.getAllByText('La sauvegarde n’est pas en place.')).toHaveLength(1)
  })

  it('says when the status cannot be read', async () => {
    mockApi({ 'GET /api/box/status': { status: 503, body: { error: 'the box status is unavailable' } } })
    render(<BoxStatus />)
    expect(await screen.findByRole('alert')).toHaveTextContent('L’état de la box est illisible : the box status is unavailable')
  })
})

describe('alertText', () => {
  it('words every alert', () => {
    const s = troubled
    expect(alertText({ kind: 'backup_disk_low' }, s)).toBe('Le disque de sauvegarde est presque plein : 900 Go libres sur 2 To.')
    expect(alertText({ kind: 'service_down', service: 'smb' }, s)).toBe('Le partage SMB ne répond plus.')
    expect(alertText({ kind: 'backup_stale' }, s)).toMatch(/^Aucune sauvegarde réussie depuis le 22 nov\. 2026/)
    expect(alertText({ kind: 'backup_stale' }, { ...s, backup: { ...s.backup, last_ok: undefined } })).toBe('Aucune sauvegarde n’a encore réussi.')
    const interrupted = { ...s, backup: { ...s.backup, last: { backup: { started_at: '2026-11-23T03:00:00Z', state: 'interrupted' as const } } } }
    expect(alertText({ kind: 'backup_failed' }, interrupted)).toMatch(/^La dernière sauvegarde, commencée le 23 nov\. 2026, .*, a été interrompue\.$/)
    expect(alertText({ kind: 'restore_test_failed' }, s)).toBe('Le dernier test de restauration a échoué.')
    expect(alertText({ kind: 'restore_test_stale' }, s)).toMatch(/^Aucun test de restauration depuis le 22 nov\. 2026/)
    expect(alertText({ kind: 'restore_test_stale' }, { ...s, backup: { ...s.backup, last: {} } })).toBe('Aucun test de restauration n’a encore tourné.')
    expect(alertText({ kind: 'smb_kept' }, { ...s, smb_kept: { waiting: 2, in_trash: 0 } })).toBe('2 fichiers envoyés par SMB attendent que la base réponde.')
    expect(alertText({ kind: 'smb_kept' }, { ...s, smb_kept: { waiting: 0, in_trash: 3 } })).toBe(
      '3 fichiers envoyés par SMB n’ont pas pu être enregistrés : ils sont dans la corbeille de leurs propriétaires.',
    )
  })

  it('rounds the free share down, so 9,9 % reads under 10 %', () => {
    expect(freePercent({ total: 1000, free: 99 })).toBe(9)
    expect(freePercent({ total: 0, free: 0 })).toBe(0)
    expect(durationLabel(42_000)).toBe('42 s')
    expect(durationLabel(4 * 60_000)).toBe('4 min')
    expect(durationLabel(65 * 60_000)).toBe('1 h 05')
  })
})
