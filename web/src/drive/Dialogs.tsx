import { ChevronLeft, Folder } from 'lucide-react'
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { messageOf } from '../api.ts'
import { join, listFolder, parentOf, type Entry } from './files.ts'

/** A modal panel; Escape or the backdrop closes it. */
export function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  return (
    <div className="backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal" role="dialog" aria-modal="true" aria-label={title}>
        <h2>{title}</h2>
        {children}
      </div>
    </div>
  )
}

/** Asks for a name, for a new folder or a rename. */
export function NameDialog({
  title,
  initial = '',
  submitLabel,
  onSubmit,
  onClose,
}: {
  title: string
  initial?: string
  submitLabel: string
  onSubmit: (name: string) => Promise<void>
  onClose: () => void
}) {
  const [name, setName] = useState(initial)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const input = useRef<HTMLInputElement>(null)

  useEffect(() => {
    const el = input.current
    if (!el) return
    el.focus()
    const dot = initial.lastIndexOf('.')
    el.setSelectionRange(0, dot > 0 ? dot : initial.length) // select the name, not the extension
  }, [initial])

  async function submit(e: FormEvent) {
    e.preventDefault()
    const clean = name.trim()
    if (!clean || clean.includes('/')) {
      setError('Un nom ne peut pas être vide ni contenir « / ».')
      return
    }
    setBusy(true)
    setError(null)
    try {
      await onSubmit(clean)
      onClose()
    } catch (err) {
      setError(messageOf(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title={title} onClose={onClose}>
      <form className="form" onSubmit={submit}>
        <label>
          Nom
          <input ref={input} value={name} onChange={(e) => setName(e.target.value)} required />
        </label>
        {error && (
          <p className="bad" role="alert">
            {error}
          </p>
        )}
        <div className="dialog-buttons">
          <button type="button" className="secondary" onClick={onClose}>
            Annuler
          </button>
          <button type="submit" disabled={busy}>
            {submitLabel}
          </button>
        </div>
      </form>
    </Modal>
  )
}

/** Browses the folders to pick where an entry goes. */
export function MoveDialog({
  entry,
  onMove,
  onClose,
}: {
  entry: Entry
  onMove: (to: string) => Promise<void>
  onClose: () => void
}) {
  const [folder, setFolder] = useState(parentOf(entry.path))
  const [folders, setFolders] = useState<Entry[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let current = true
    listFolder(folder)
      .then((list) => current && setFolders(list.filter((e) => e.kind === 'dir' && e.path !== entry.path)))
      .catch((err: unknown) => current && setError(messageOf(err)))
    return () => {
      current = false
    }
  }, [folder, entry.path])

  const here = folder === parentOf(entry.path)
  async function move() {
    setBusy(true)
    setError(null)
    try {
      await onMove(join(folder, entry.name))
      onClose()
    } catch (err) {
      setError(messageOf(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal title={`Déplacer « ${entry.name} »`} onClose={onClose}>
      <p className="picker-path">
        {folder ? (
          <button type="button" className="icon-button" aria-label="Dossier parent" onClick={() => setFolder(parentOf(folder))}>
            <ChevronLeft size={16} aria-hidden />
          </button>
        ) : null}
        <strong>{folder ? folder.split('/').pop() : 'Mon drive'}</strong>
      </p>
      <ul className="picker">
        {folders === null && <li className="muted">Chargement…</li>}
        {folders?.length === 0 && <li className="muted">Aucun sous-dossier.</li>}
        {folders?.map((f) => (
          <li key={f.path}>
            <button type="button" onClick={() => setFolder(f.path)}>
              <Folder size={16} aria-hidden className="icon-folder" /> {f.name}
            </button>
          </li>
        ))}
      </ul>
      {error && (
        <p className="bad" role="alert">
          {error}
        </p>
      )}
      <div className="dialog-buttons">
        <button type="button" className="secondary" onClick={onClose}>
          Annuler
        </button>
        <button type="button" onClick={move} disabled={busy || here}>
          Déplacer ici
        </button>
      </div>
    </Modal>
  )
}
