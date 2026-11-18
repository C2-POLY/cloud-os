/// <reference types="node" />
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { hashChunks } from '../backup/hash'
import { emptyLedger, loadLedger, saveLedger, type KeyValue, type Ledger } from '../backup/ledger'
import { destination, folderName, variant } from '../backup/paths'
import { KNOWN_BATCH, runBackup, type Deps, type PhoneAsset } from '../backup/run'

jest.mock('expo-file-system', () => ({ File: class {} }))

const bytes = (s: string) => new TextEncoder().encode(s)

describe('hashChunks', () => {
  it('hashes chunk by chunk to the known SHA-256, and closes the file', async () => {
    const data = bytes('abc')
    let offset = 0
    let closed = false
    const src = {
      readBytes: async (n: number) => {
        const out = data.slice(offset, offset + n)
        offset += out.length
        return out
      },
      close: () => {
        closed = true
      },
    }
    expect(await hashChunks(src, 1)).toBe('ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad')
    expect(closed).toBe(true)
  })
})

describe('ledger', () => {
  const memory = (): KeyValue & { data: Map<string, string> } => {
    const data = new Map<string, string>()
    return { data, getItem: async (k) => data.get(k) ?? null, setItem: async (k, v) => void data.set(k, v) }
  }

  it('starts empty, keeps what it saved, and starts again from a damaged copy', async () => {
    const kv = memory()
    expect(await loadLedger(kv)).toEqual(emptyLedger())
    const l: Ledger = { items: { a: { sha256: 'ab', done: true } }, lastSuccess: 5, lastError: null }
    await saveLedger(kv, l)
    expect(await loadLedger(kv)).toEqual(l)
    for (const k of kv.data.keys()) kv.data.set(k, '{not json')
    expect(await loadLedger(kv)).toEqual(emptyLedger())
  })
})

describe('paths', () => {
  it('files a photo by phone, year and month, and numbers a taken name', () => {
    const taken = new Date(2024, 6, 14, 18, 3).getTime()
    expect(destination("iPhone d'Enzo", 'IMG_0001.HEIC', taken)).toBe("Photos/iPhone d'Enzo/2024/07/IMG_0001.HEIC")
    expect(variant('Photos/a/2024/07/IMG_0001.HEIC', 1)).toBe('Photos/a/2024/07/IMG_0001.HEIC')
    expect(variant('Photos/a/2024/07/IMG_0001.HEIC', 2)).toBe('Photos/a/2024/07/IMG_0001 (2).HEIC')
    expect(variant('Photos/a.b/2024/07/NOEXT', 3)).toBe('Photos/a.b/2024/07/NOEXT (3)')
  })

  it('keeps a folder name to one safe segment', () => {
    expect(folderName('Pixel/8 Pro')).toBe('Pixel-8 Pro')
    expect(folderName('..hidden')).toBe('hidden')
    expect(folderName('  ')).toBe('Téléphone')
    expect(folderName(null)).toBe('Téléphone')
  })
})

describe('runBackup', () => {
  const day = new Date(2024, 6, 14).getTime()
  const asset = (id: string, filename = `${id}.jpg`): PhoneAsset => ({ id, filename, takenMs: day })

  /** A phone and a box in memory: contents by uri, files by path. */
  function world(roll: PhoneAsset[], content: Record<string, string>, onBox: string[] = []) {
    const box = new Map<string, string>() // path → sha
    const sha = (id: string) => `sha-${content[id]}`
    const calls = { hash: 0, known: [] as number[], upload: [] as string[] }
    let failUploads = false
    let clock = 0
    const deps: Deps = {
      assets: async () => roll,
      localUri: async (a) => `file://${a.id}`,
      hash: async (uri) => {
        calls.hash++
        return sha(uri.slice('file://'.length))
      },
      known: async (sums) => {
        calls.known.push(sums.length)
        const have = new Set([...box.values(), ...onBox])
        return sums.filter((s) => have.has(s))
      },
      upload: async (_uri, path, s) => {
        if (failUploads) throw new Error('Network request failed')
        calls.upload.push(path)
        if (box.has(path)) return 409
        box.set(path, s)
        return 201
      },
      save: async () => {},
      now: () => clock,
    }
    return {
      deps,
      box,
      calls,
      breakNetwork: (on: boolean) => {
        failUploads = on
      },
      setClock: (t: number) => {
        clock = t
      },
    }
  }

  it('sends what the box lacks to the phone folder, skips what it has, then sends nothing more', async () => {
    const w = world([asset('a'), asset('b'), asset('c')], { a: '1', b: '2', c: '3' }, ['sha-2'])
    const ledger = emptyLedger()
    const s = await runBackup(w.deps, ledger, 'iPhone', Infinity)
    expect(s).toMatchObject({ total: 3, sent: 2, alreadyThere: 1, remaining: 0, stopped: 'done' })
    expect([...w.box.keys()]).toEqual(['Photos/iPhone/2024/07/a.jpg', 'Photos/iPhone/2024/07/c.jpg'])
    expect(ledger.lastSuccess).not.toBeNull()

    const again = await runBackup(w.deps, ledger, 'iPhone', Infinity)
    expect(again).toMatchObject({ sent: 0, remaining: 0 })
    expect(w.calls.hash).toBe(3) // nothing hashed twice
    expect(w.calls.upload).toHaveLength(2)
  })

  it('never replaces a file on the box: a taken name gets " (2)"', async () => {
    const w = world([asset('a', 'IMG_0001.JPG')], { a: '1' })
    w.box.set('Photos/iPhone/2024/07/IMG_0001.JPG', 'sha-other')
    await runBackup(w.deps, emptyLedger(), 'iPhone')
    expect(w.box.get('Photos/iPhone/2024/07/IMG_0001.JPG')).toBe('sha-other')
    expect(w.box.get('Photos/iPhone/2024/07/IMG_0001 (2).JPG')).toBe('sha-1')
  })

  it('sends a content twice in the roll only once', async () => {
    const w = world([asset('a'), asset('b')], { a: 'same', b: 'same' })
    const s = await runBackup(w.deps, emptyLedger(), 'iPhone')
    expect(w.calls.upload).toEqual(['Photos/iPhone/2024/07/a.jpg'])
    expect(s.remaining).toBe(0)
  })

  it('stops at the deadline and resumes where it stopped, without hashing again', async () => {
    const w = world([asset('a'), asset('b')], { a: '1', b: '2' })
    const ledger = emptyLedger()
    w.setClock(10)
    const first = await runBackup(w.deps, ledger, 'iPhone', 10)
    expect(first).toMatchObject({ stopped: 'deadline', sent: 0, remaining: 2 })
    expect(ledger.lastSuccess).toBeNull()

    w.setClock(20)
    const second = await runBackup(w.deps, ledger, 'iPhone', Infinity)
    expect(second).toMatchObject({ stopped: 'done', sent: 2, remaining: 0 })
    expect(w.calls.hash).toBe(2)
  })

  it('keeps an element to send after a network error, and sends it on the next pass', async () => {
    const w = world([asset('a')], { a: '1' })
    const ledger = emptyLedger()
    w.breakNetwork(true)
    const cut = await runBackup(w.deps, ledger, 'iPhone')
    expect(cut).toMatchObject({ stopped: 'error', error: 'Network request failed', remaining: 1 })
    expect(ledger.lastError).toBe('Network request failed')

    w.breakNetwork(false)
    const next = await runBackup(w.deps, ledger, 'iPhone')
    expect(next).toMatchObject({ stopped: 'done', sent: 1, remaining: 0 })
    expect(ledger.lastError).toBeNull()
  })

  it(`asks the box by batches of ${KNOWN_BATCH}`, async () => {
    const roll = Array.from({ length: 2500 }, (_, i) => asset(`p${i}`))
    const content = Object.fromEntries(roll.map((a) => [a.id, a.id]))
    const w = world(roll, content, roll.map((a) => `sha-${a.id}`))
    const s = await runBackup(w.deps, emptyLedger(), 'iPhone')
    expect(w.calls.known).toEqual([1000, 1000, 500])
    expect(s).toMatchObject({ alreadyThere: 2500, sent: 0 })
  })
})

describe('the camera roll', () => {
  it('is only read: the backup imports nothing that deletes or edits an element', () => {
    const dir = join(__dirname, '..', 'backup')
    const files = readdirSync(dir)
      .filter((f) => /\.tsx?$/.test(f))
      .map((f) => join(dir, f))
      .concat(join(__dirname, '..', 'app', '(tabs)', 'backup.tsx'))
    expect(files.length).toBeGreaterThan(5)
    const allowed = new Set(['getAssetsAsync', 'getAssetInfoAsync', 'getPermissionsAsync', 'requestPermissionsAsync', 'MediaType'])
    for (const file of files) {
      const src = readFileSync(file, 'utf8')
      for (const m of src.matchAll(/import\s*\{([^}]*)\}\s*from\s*'expo-media-library[^']*'/g)) {
        for (const name of m[1].split(',').map((n) => n.trim()).filter(Boolean)) expect(allowed).toContain(name)
      }
      expect(src).not.toMatch(/from 'expo-media-library[^']*'.*\*|import \* as \w+ from 'expo-media-library/)
      expect(src).not.toMatch(/deleteAssets|removeAssets|\.delete\(|saveToLibrary|createAsset/)
    }
  })
})
