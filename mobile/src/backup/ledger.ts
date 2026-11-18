/** One element of the camera roll, as this phone remembers it between passes. */
export interface Item {
  sha256: string // hex, once hashed
  done: boolean // on the box, sent by this phone or already there
}

/** What the backup remembers, so that a pass cut short resumes without hashing again. */
export interface Ledger {
  items: Record<string, Item> // by media library id
  lastSuccess: number | null // ms since epoch: the last pass that left nothing to send
  lastError: string | null
}

/** Where the ledger is kept (AsyncStorage on the phone). */
export interface KeyValue {
  getItem(key: string): Promise<string | null>
  setItem(key: string, value: string): Promise<void>
}

const key = 'cloudos.backup.ledger'

export function emptyLedger(): Ledger {
  return { items: {}, lastSuccess: null, lastError: null }
}

export async function loadLedger(store: KeyValue): Promise<Ledger> {
  const raw = await store.getItem(key)
  if (!raw) return emptyLedger()
  try {
    const parsed = JSON.parse(raw) as Partial<Ledger>
    return { ...emptyLedger(), ...parsed, items: parsed.items ?? {} }
  } catch {
    return emptyLedger() // a damaged ledger only costs hashing again; the box dedupes
  }
}

export function saveLedger(store: KeyValue, ledger: Ledger): Promise<void> {
  return store.setItem(key, JSON.stringify(ledger))
}
