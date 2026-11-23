import { useEffect, useSyncExternalStore } from 'react'
import type { BackupRun, BoxAlert, BoxService, BoxStatus, DiskSpace, RunState, ServiceState } from '../../../shared/api-types.ts'
import { api, messageOf } from '../api.ts'
import { formatDate, formatSize } from '../drive/files.ts'

export type { BackupRun, BoxAlert, BoxService, BoxStatus, DiskSpace, RunState, ServiceState }

/** The address of « État de la box ». */
export const statusPath = '/etat'

/** How often the status is read again, as the widgets do; and when the tab comes back into view. */
export const REFRESH_MS = 30_000

export function fetchBoxStatus(): Promise<BoxStatus> {
  return api<BoxStatus>('GET', '/api/box/status')
}

type Snapshot = { status: BoxStatus | null; error: string | null }

// One reading shared by the menu bar's badge and the panel: one request every
// 30 s while either is shown, none once both are gone (and nothing kept for
// the next account to sign in).
const empty: Snapshot = { status: null, error: null }
let snapshot = empty
let reading: Promise<void> | null = null
let timer: number | undefined
// A reading started before the last listener left is not shown to the next.
let generation = 0
const listeners = new Set<() => void>()

function publish(gen: number, next: (s: Snapshot) => Snapshot) {
  if (gen !== generation || listeners.size === 0) return
  snapshot = next(snapshot)
  listeners.forEach((l) => l())
}

/** Reads the status now (one request at a time); a failed read keeps what is shown and says why. */
export function refreshBoxStatus(): Promise<void> {
  const gen = generation
  reading ??= fetchBoxStatus()
    .then((status) => publish(gen, () => ({ status, error: null })))
    .catch((err: unknown) => publish(gen, (s) => ({ status: s.status, error: messageOf(err) })))
    .then(() => {
      if (gen === generation) reading = null
    })
  return reading
}

function onVisible() {
  if (document.visibilityState === 'visible') void refreshBoxStatus()
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  if (listeners.size === 1) {
    void refreshBoxStatus()
    timer = window.setInterval(() => void refreshBoxStatus(), REFRESH_MS)
    document.addEventListener('visibilitychange', onVisible)
  }
  return () => {
    listeners.delete(listener)
    if (listeners.size === 0) {
      window.clearInterval(timer)
      document.removeEventListener('visibilitychange', onVisible)
      generation++
      reading = null
      snapshot = empty
    }
  }
}

const read = () => snapshot

/** The box's status, kept up to date while it is shown; `enabled` false (a member) never asks. */
export function useBoxStatus(enabled = true): Snapshot {
  return useSyncExternalStore(enabled ? subscribe : noSubscribe, enabled ? read : readEmpty)
}

const noSubscribe = () => () => {}
const readEmpty = () => empty

/** Reads the status again when the panel opens: the badge's reading may be 30 s old. */
export function useFreshBoxStatus(): Snapshot {
  const s = useBoxStatus()
  useEffect(() => {
    void refreshBoxStatus()
  }, [])
  return s
}

const plural = (n: number, one: string, many: string) => `${n} ${n > 1 ? many : one}`

/** « 2 alertes ». */
export function alertsLabel(n: number): string {
  return plural(n, 'alerte', 'alertes')
}

const serviceNames: Record<BoxService['name'], string> = {
  core: 'Le cœur',
  worker: 'Le worker (analyse et recherche)',
  smb: 'Le partage SMB',
  tailscale: 'Tailscale (accès hors de la maison)',
}

/** « 12 Go libres sur 500 Go ». */
export function spaceLabel(d: DiskSpace): string {
  return `${formatSize(d.free)} libres sur ${formatSize(d.total)}`
}

/** The share of a disk that is free, in whole percent (rounded down: 9,9 % is 9 %, under the 10 % threshold). */
export function freePercent(d: DiskSpace): number {
  return d.total > 0 ? Math.floor((d.free * 100) / d.total) : 0
}

/** What an alert says, in French, with what the status knows of it. */
export function alertText(a: BoxAlert, s: BoxStatus): string {
  switch (a.kind) {
    case 'data_disk_low':
      return `Le disque des données est presque plein${s.data_disk ? ` : ${spaceLabel(s.data_disk)}` : ''}.`
    case 'backup_disk_low':
      return `Le disque de sauvegarde est presque plein${s.backup_disk ? ` : ${spaceLabel(s.backup_disk)}` : ''}.`
    case 'service_down':
      return `${a.service ? serviceNames[a.service] : 'Un service'} ne répond plus.`
    case 'backup_failed': {
      const run = s.backup.last.backup
      if (run?.state === 'interrupted') return `La dernière sauvegarde, commencée le ${formatDate(run.started_at)}, a été interrompue.`
      return `La dernière sauvegarde a échoué${run ? ` (${formatDate(run.finished_at ?? run.started_at)})` : ''}.`
    }
    case 'backup_stale':
      return s.backup.last_ok
        ? `Aucune sauvegarde réussie depuis le ${formatDate(s.backup.last_ok)}.`
        : 'Aucune sauvegarde n’a encore réussi.'
    case 'restore_test_failed':
      return 'Le dernier test de restauration a échoué.'
    case 'restore_test_stale': {
      const run = s.backup.last.restore_test
      return run ? `Aucun test de restauration depuis le ${formatDate(run.started_at)}.` : 'Aucun test de restauration n’a encore tourné.'
    }
    case 'smb_kept': {
      const { waiting = 0, in_trash } = s.smb_kept
      if (waiting > 0) return `${plural(waiting, 'fichier envoyé par SMB attend', 'fichiers envoyés par SMB attendent')} que la base réponde.`
      return in_trash > 1
        ? `${in_trash} fichiers envoyés par SMB n’ont pas pu être enregistrés : ils sont dans la corbeille de leurs propriétaires.`
        : 'Un fichier envoyé par SMB n’a pas pu être enregistré : il est dans la corbeille de son propriétaire.'
    }
  }
}

/** A service's state in a word. */
export function stateLabel(state: ServiceState | undefined, on = 'répond'): string {
  switch (state) {
    case 'ok':
      return on
    case 'down':
      return 'ne répond plus'
    case 'off':
    case undefined:
      return 'éteint'
  }
}

/** Where a run stands, in a word. */
export function runLabel(state: RunState): string {
  switch (state) {
    case 'ok':
      return 'réussi'
    case 'failed':
      return 'échoué'
    case 'running':
      return 'en cours'
    case 'interrupted':
      return 'interrompu'
  }
}

/** « 1 234 fichiers, 12 Go, 4 min » for a backup that ran. */
export function backupFacts(r: BackupRun): string {
  const parts: string[] = []
  if (r.files !== undefined) parts.push(plural(r.files, 'fichier', 'fichiers'))
  if (r.bytes !== undefined) parts.push(formatSize(r.bytes))
  if (r.data_added !== undefined) parts.push(`${formatSize(r.data_added)} ajoutés`)
  if (r.duration_ms !== undefined) parts.push(durationLabel(r.duration_ms))
  return parts.join(', ')
}

/** « 42 s », « 4 min », « 1 h 05 ». */
export function durationLabel(ms: number): string {
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s} s`
  const m = Math.round(s / 60)
  if (m < 60) return `${m} min`
  return `${Math.floor(m / 60)} h ${String(m % 60).padStart(2, '0')}`
}
