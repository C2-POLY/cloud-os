import type { Ledger } from './ledger'
import { destination, variant } from './paths'

/** An element of the camera roll: a photo or a video. */
export interface PhoneAsset {
  id: string
  filename: string
  takenMs: number // creation time, ms since epoch
}

/** What a pass needs from the phone and the box; the real ones are in task.ts, fakes in the tests. */
export interface Deps {
  assets(): Promise<PhoneAsset[]>
  localUri(asset: PhoneAsset): Promise<string> // "file://…", downloaded from iCloud if need be
  hash(uri: string): Promise<string>
  known(sha256: string[]): Promise<string[]> // at most KNOWN_BATCH at a time
  upload(uri: string, path: string, sha256: string, takenMs: number): Promise<number> // HTTP status
  save(ledger: Ledger): Promise<void>
  now(): number
}

export const KNOWN_BATCH = 1000 // POST /api/files/known takes at most 1000 sums
const MAX_VARIANTS = 50

/** What a pass did, and what is left. */
export interface Summary {
  total: number
  sent: number
  alreadyThere: number
  remaining: number
  stopped: 'done' | 'deadline' | 'error'
  error?: string
}

/**
 * One backup pass: hash what is new, ask the box which contents it already
 * has, send the others to Photos/<phone>/YYYY/MM/. It never replaces a file
 * on the box (a taken name gets " (2)") and never touches the camera roll.
 * It stops at the deadline (ms since epoch) or at the first network error;
 * the ledger, saved as it goes, lets the next pass resume.
 */
export async function runBackup(deps: Deps, ledger: Ledger, phone: string, deadline = Infinity, onProgress?: (s: Summary) => void): Promise<Summary> {
  const assets = await deps.assets()
  const summary: Summary = { total: assets.length, sent: 0, alreadyThere: 0, remaining: 0, stopped: 'done' }
  const left = () => assets.filter((a) => !ledger.items[a.id]?.done).length
  const report = () => {
    summary.remaining = left()
    onProgress?.({ ...summary })
  }
  const uris = new Map<string, string>()
  const fail = async (err: unknown) => {
    summary.stopped = 'error'
    summary.error = err instanceof Error ? err.message : String(err)
    ledger.lastError = summary.error
    await deps.save(ledger)
    summary.remaining = left()
    return summary
  }
  try {
    // 1. Hash what has no sum yet.
    for (const a of assets) {
      if (ledger.items[a.id]) continue
      if (deps.now() >= deadline) {
        summary.stopped = 'deadline'
        break
      }
      const uri = await deps.localUri(a)
      uris.set(a.id, uri)
      ledger.items[a.id] = { sha256: await deps.hash(uri), done: false }
    }
    await deps.save(ledger)

    // 2. Ask the box which of the pending contents it already has.
    const pending = assets.filter((a) => ledger.items[a.id] && !ledger.items[a.id].done)
    const sums = [...new Set(pending.map((a) => ledger.items[a.id].sha256))]
    const there = new Set<string>()
    for (let i = 0; i < sums.length; i += KNOWN_BATCH) {
      for (const s of await deps.known(sums.slice(i, i + KNOWN_BATCH))) there.add(s)
    }
    for (const a of pending) {
      if (there.has(ledger.items[a.id].sha256)) {
        ledger.items[a.id].done = true
        summary.alreadyThere++
      }
    }
    await deps.save(ledger)
    report()

    // 3. Send the others, one at a time.
    for (const a of pending) {
      const item = ledger.items[a.id]
      if (item.done) continue
      if (there.has(item.sha256)) {
        item.done = true // a duplicate in the roll, sent earlier in this pass
        continue
      }
      if (deps.now() >= deadline) {
        summary.stopped = 'deadline'
        break
      }
      const uri = uris.get(a.id) ?? (await deps.localUri(a))
      const base = destination(phone, a.filename, a.takenMs)
      let status = 409
      for (let n = 1; n <= MAX_VARIANTS && status === 409; n++) {
        status = await deps.upload(uri, variant(base, n), item.sha256, a.takenMs)
      }
      if (status !== 201) throw new Error(`La box a refusé ${a.filename} (HTTP ${status}).`)
      item.done = true
      there.add(item.sha256)
      summary.sent++
      await deps.save(ledger)
      report()
    }
  } catch (err) {
    return fail(err)
  }
  summary.remaining = left()
  if (summary.remaining === 0) {
    ledger.lastSuccess = deps.now()
    ledger.lastError = null
  }
  await deps.save(ledger)
  return summary
}
