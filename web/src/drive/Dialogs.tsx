import { ChevronLeft, Download, Folder, RotateCcw } from 'lucide-react'
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { messageOf } from '../api.ts'
import { formatDate, formatDateSeconds, formatSize, join, listFolder, listVersions, parentOf, restoreVersion, versionURL, type Entry, type VersionEntry } from './files.ts'

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

/** Asks before an action that cannot be undone. */
export function ConfirmDialog({
  title,
  message,
  confirmLabel,
  onConfirm,
  onClose,
}: {
  title: string
  message: string
  confirmLabel: string
  onConfirm: () => Promise<void>
  onClose: () => void
}) {
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  return (
    <Modal title={title} onClose={onClose}>
      <p>{message}</p>
      {error && (
        <p className="bad" role="alert">
          {error}
        </p>
      )}
      <div className="dialog-buttons">
        <button type="button" className="secondary" onClick={onClose} autoFocus>
          Annuler
        </button>
        <button
          type="button"
          className="danger"
          disabled={busy}
          onClick={async () => {
            setBusy(true)
            setError(null)
            try {
              await onConfirm()
              onClose()
            } catch (err) {
              setError(messageOf(err))
            } finally {
              setBusy(false)
            }
          }}
        >
          {confirmLabel}
        </button>
      </div>
    </Modal>
  )
}

/** The versions of a file: download one, or make it the current content again. */
export function VersionsDialog({ entry, onRestored, onClose }: { entry: Entry; onRestored: () => void; onClose: () => void }) {
  const [versions, setVersions] = useState<VersionEntry[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [version, setVersion] = useState(0)

  useEffect(() => {
    let current = true
    listVersions(entry.path)
      .then((list) => current && setVersions(list))
      .catch((err: unknown) => current && setError(messageOf(err)))
    return () => {
      current = false
    }
  }, [entry.path, version])

  return (
    <Modal title={`Versions de « ${entry.name} »`} onClose={onClose}>
      <p className="muted">
        Version actuelle : {formatSize(entry.size)}, modifiée le {formatDate(entry.modified_at)}.
      </p>
      {versions?.length === 0 && <p className="muted">Aucune version précédente : ce fichier n'a jamais été remplacé.</p>}
      {versions && versions.length > 0 && (
        <ul className="picker versions">
          {versions.map((v) => (
            <li key={v.id}>
              <span className="version-text">
                <strong>{formatDateSeconds(v.modified_at)}</strong>
                <span className="muted">
                  {formatSize(v.size)} · remplacée le {formatDateSeconds(v.replaced_at)}
                </span>
              </span>
              <span className="row-actions always">
                <a className="icon-button" href={versionURL(v.id, entry.name)} download={entry.name} aria-label={`Télécharger la version du ${formatDateSeconds(v.modified_at)}`} title="Télécharger">
                  <Download size={16} aria-hidden />
                </a>
                <button
                  type="button"
                  className="icon-button"
                  title="Restaurer"
                  aria-label={`Restaurer la version du ${formatDateSeconds(v.modified_at)}`}
                  onClick={async () => {
                    try {
                      await restoreVersion(v.id)
                      setNotice('Version restaurée. Le contenu remplacé est gardé comme version.')
                      setVersion((n) => n + 1)
                      onRestored()
                    } catch (err) {
                      setError(messageOf(err))
                    }
                  }}
                >
                  <RotateCcw size={16} aria-hidden />
                </button>
              </span>
            </li>
          ))}
        </ul>
      )}
      {notice && (
        <p className="good" role="status">
          {notice}
        </p>
      )}
      {error && (
        <p className="bad" role="alert">
          {error}
        </p>
      )}
      <div className="dialog-buttons">
        <button type="button" className="secondary" onClick={onClose}>
          Fermer
        </button>
      </div>
    </Modal>
  )
}
