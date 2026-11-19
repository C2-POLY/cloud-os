import * as tus from 'tus-js-client'
import { useSyncExternalStore } from 'react'
import { FILE_STORED } from '../analysis/analysis.ts'

// The upload queue: files dropped in the drive go to the core with tus, two at
// a time. tus-js-client keeps each upload's URL in localStorage, so a cut
// connection or a reloaded page resumes at the right byte.

export type UploadState = 'waiting' | 'sending' | 'paused' | 'retrying' | 'done' | 'failed' | 'cancelled'

export interface UploadItem {
  id: number
  name: string
  path: string // where the file goes in the drive
  size: number
  sent: number
  state: UploadState
  error?: string
  /** The name was taken: the user may choose to replace the existing file. */
  canReplace?: boolean
  replace?: boolean
}

/** The part of tus.Upload the queue uses, so tests can stand in for it. */
export interface TusUpload {
  start(): void
  abort(terminate?: boolean): Promise<void>
  findPreviousUploads(): Promise<tus.PreviousUpload[]>
  resumeFromPreviousUpload(previous: tus.PreviousUpload): void
}

export type TusFactory = (file: File, options: tus.UploadOptions) => TusUpload

const PARALLEL = 2
const CHUNK = 8 * 1024 * 1024

/** French wording for a failed upload, from the core's status. */
export function uploadError(err: unknown): string {
  const status = (err as tus.DetailedError)?.originalResponse?.getStatus?.()
  switch (status) {
    case 409:
      return 'Un fichier ou un dossier porte déjà ce nom.'
    case 413:
      return 'Fichier trop gros pour la place libre de la box.'
    case 422:
      return 'Le fichier reçu ne correspond pas à l’original.'
    case 401:
      return 'Session expirée, reconnectez-vous.'
    case 400:
      return 'Nom de fichier refusé.'
    default:
      return 'Envoi impossible pour le moment.'
  }
}

/**
 * Whether tus should try again after err. Only a cut connection (no answer),
 * a server error, a busy upload (423) or a chunk out of step (409 on PATCH)
 * are worth retrying; a refused creation (name taken, too large, bad name)
 * fails at once so the panel can explain it and offer Remplacer.
 */
export function shouldRetry(err: unknown): boolean {
  const e = err as tus.DetailedError
  const status = e?.originalResponse?.getStatus?.()
  if (status === undefined || status === 0 || status >= 500) return true
  if (status === 423) return true
  return status === 409 && e?.originalRequest?.getMethod?.() === 'PATCH'
}

export class UploadQueue {
  private items: UploadItem[] = []
  private uploads = new Map<number, TusUpload>()
  private files = new Map<number, File>()
  private listeners = new Set<() => void>()
  private nextId = 1
  private readonly factory: TusFactory
  private storedListeners = new Set<(folder: string) => void>()

  constructor(factory: TusFactory = (file, options) => new tus.Upload(file, options)) {
    this.factory = factory
  }

  subscribe = (listener: () => void) => {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  snapshot = () => this.items

  /** Calls listener with a file's folder each time a file is stored; returns the unsubscribe. */
  onStored(listener: (folder: string) => void): () => void {
    this.storedListeners.add(listener)
    return () => this.storedListeners.delete(listener)
  }

  private set(id: number, change: Partial<UploadItem>) {
    this.items = this.items.map((it) => (it.id === id ? { ...it, ...change } : it))
    this.listeners.forEach((l) => l())
  }

  /** Queues files for the folder. */
  add(files: Iterable<File>, folder: string) {
    for (const file of files) {
      const id = this.nextId++
      const path = folder ? `${folder}/${file.name}` : file.name
      this.files.set(id, file)
      this.items = [...this.items, { id, name: file.name, path, size: file.size, sent: 0, state: 'waiting' }]
    }
    this.listeners.forEach((l) => l())
    this.pump()
  }

  /** Starts waiting uploads while fewer than PARALLEL are running. */
  private pump() {
    const running = this.items.filter((it) => it.state === 'sending' || it.state === 'retrying').length
    const waiting = this.items.filter((it) => it.state === 'waiting').slice(0, Math.max(0, PARALLEL - running))
    for (const it of waiting) void this.start(it.id)
  }

  private async start(id: number) {
    const item = this.items.find((it) => it.id === id)
    const file = this.files.get(id)
    if (!item || !file) return
    this.set(id, { state: 'sending', error: undefined })

    let upload = this.uploads.get(id)
    if (!upload) {
      upload = this.factory(file, {
        endpoint: '/api/uploads',
        chunkSize: CHUNK,
        retryDelays: [1000, 3000, 5000, 10000],
        headers: { 'X-Cloudos-Client': 'web' },
        metadata: {
          path: item.path,
          filename: file.name,
          mtime: String(Math.floor(file.lastModified / 1000)),
          ...(item.replace ? { replace: '1' } : {}),
        },
        // One fingerprint per destination, so the same file sent to two folders resumes separately.
        fingerprint: async () => `cloudos:${item.replace ? 'replace:' : ''}${item.path}:${file.size}:${file.lastModified}`,
        removeFingerprintOnSuccess: true,
        onProgress: (sent) => this.set(id, { sent, state: 'sending' }),
        onShouldRetry: (err) => {
          if (!shouldRetry(err)) return false
          this.set(id, { state: 'retrying' })
          return true
        },
        onSuccess: () => {
          this.set(id, { sent: file.size, state: 'done' })
          window.dispatchEvent(new Event(FILE_STORED))
          this.uploads.delete(id)
          this.files.delete(id)
          const folder = item.path.includes('/') ? item.path.slice(0, item.path.lastIndexOf('/')) : ''
          this.storedListeners.forEach((l) => l(folder))
          this.pump()
        },
        onError: (err) => {
          const conflict = (err as tus.DetailedError)?.originalResponse?.getStatus?.() === 409
          this.set(id, { state: 'failed', error: uploadError(err), canReplace: conflict && !item.replace })
          this.uploads.delete(id)
          this.pump()
        },
      })
      this.uploads.set(id, upload)
      const previous = await upload.findPreviousUploads()
      if (previous.length > 0) upload.resumeFromPreviousUpload(previous[0])
    }
    upload.start()
  }

  pause(id: number) {
    const upload = this.uploads.get(id)
    if (!upload) return
    void upload.abort(false)
    this.set(id, { state: 'paused' })
    this.pump()
  }

  resume(id: number) {
    this.set(id, { state: 'waiting' })
    this.pump()
  }

  /** Sends a file whose name was taken again, replacing the existing file; its content becomes a version. */
  replace(id: number) {
    if (!this.files.has(id)) return
    this.uploads.delete(id)
    this.set(id, { state: 'waiting', error: undefined, canReplace: false, replace: true, sent: 0 })
    this.pump()
  }

  /** Stops an upload and has the core drop its partial file; nothing stored is touched. */
  cancel(id: number) {
    const upload = this.uploads.get(id)
    if (upload) void upload.abort(true)
    this.uploads.delete(id)
    this.files.delete(id)
    this.set(id, { state: 'cancelled' })
    this.pump()
  }

  /** Forgets finished, failed and cancelled uploads. */
  clear() {
    this.items = this.items.filter((it) => !['done', 'failed', 'cancelled'].includes(it.state))
    this.listeners.forEach((l) => l())
  }
}

export const uploadQueue = new UploadQueue()

export function useUploads(queue: UploadQueue = uploadQueue): UploadItem[] {
  return useSyncExternalStore(queue.subscribe, queue.snapshot)
}
